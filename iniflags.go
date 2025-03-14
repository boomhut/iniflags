package iniflags

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

var (
	allowUnknownFlags      = flag.Bool("allowUnknownFlags", false, "Don't terminate the application if ini file contains unknown flags.")
	allowMissingConfig     = flag.Bool("allowMissingConfig", false, "Don't terminate the application if the ini file cannot be read.")
	config                 = flag.String("config", "", "Path to ini config. May be relative to the current executable path.")
	configUpdateInterval   = flag.Duration("configUpdateInterval", 0, "Update interval for re-reading config file set via -config flag. Zero disables config file re-reading.")
	dumpflags              = flag.Bool("dumpflags", false, "Dumps values for all flags defined in the application into stdout in ini-compatible syntax and terminates the app.")
	unsecure               = flag.Bool("unsecure", false, "Allow unsecure communication with the server when loading config file via http.")
	originalUsage          = flag.Usage // Store the original usage function
	flagsToExcludeFromDump = map[string]bool{
		"config":               true,
		"dumpflags":            true,
		"allowUnknownFlags":    true,
		"allowMissingConfig":   true,
		"configUpdateInterval": true,
		"unsecure":             true,
	}
)

var (
	flagChangeCallbacks   = make(map[string][]FlagChangeCallback)
	importStack           []string
	parsed                bool
	flagShorthands        = make(map[string]string) // Maps shorthand name to full flag name
	commandLineShorthands = make(map[string]bool)   // Tracks which shorthands are registered for command line use
)

// Generation is flags' generation number.
//
// It is modified on each flags' modification
// via either -configUpdateInterval or SIGHUP.
var Generation int

// Parse obtains flag values from config file set via -config.
//
// It obtains flag values from command line like flag.Parse(), then overrides
// them by values parsed from config file set via -config.
//
// Path to config file can also be set via SetConfigFile() before Parse() call.
func Parse() {
	if parsed {
		logger.Panicf("iniflags: duplicate call to iniflags.Parse() detected")
	}

	// Set custom usage function to include shorthands
	flag.Usage = customUsage

	// Handle command-line shorthands before calling flag.Parse()
	handleCommandLineShorthands()

	parsed = true
	flag.Parse()
	_, ok := parseConfigFlags()
	if !ok {
		os.Exit(1)
	}

	if *dumpflags {
		dumpFlags()
		os.Exit(0)
	}

	for flagName := range flagChangeCallbacks {
		verifyFlagChangeFlagName(flagName)
	}
	Generation++
	issueAllFlagChangeCallbacks()

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go sighupHandler(ch)

	go configUpdater()
}

// handleCommandLineShorthands processes command-line arguments and
// replaces registered shorthands with their full flag names
func handleCommandLineShorthands() {
	if len(commandLineShorthands) == 0 {
		return
	}

	args := make([]string, 0, len(os.Args))
	args = append(args, os.Args[0])

	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]

		// Check if the argument is a shorthand flag
		if len(arg) > 1 && arg[0] == '-' && !strings.HasPrefix(arg, "--") {
			// Remove the leading dash
			shortName := arg[1:]

			// If there's an equals sign, split it
			value := ""
			hasValue := false
			if pos := strings.Index(shortName, "="); pos >= 0 {
				value = shortName[pos:]
				shortName = shortName[:pos]
				hasValue = true
			}

			// Check if it's a registered command-line shorthand
			if fullName, exists := flagShorthands[shortName]; exists && commandLineShorthands[shortName] {
				// Replace with full name
				if hasValue {
					args = append(args, "-"+fullName+value)
				} else {
					args = append(args, "-"+fullName)
					// If the next arg doesn't start with a dash, it's probably the value
					if i+1 < len(os.Args) && !strings.HasPrefix(os.Args[i+1], "-") {
						args = append(args, os.Args[i+1])
						i++
					}
				}
			} else {
				// Not a registered shorthand or not for command line, pass through unchanged
				args = append(args, arg)
			}
		} else {
			// Not a shorthand flag, pass through unchanged
			args = append(args, arg)
		}
	}

	// Replace os.Args with the processed arguments
	os.Args = args
}

func configUpdater() {
	if *configUpdateInterval != 0 {
		for {
			// Use time.Sleep() instead of time.Tick() for the sake of dynamic flag update.
			time.Sleep(*configUpdateInterval)
			updateConfig()
		}
	}
}

func updateConfig() {
	if oldFlagValues, ok := parseConfigFlags(); ok && len(oldFlagValues) > 0 {
		modifiedFlags := make(map[string]string)
		for k := range oldFlagValues {
			modifiedFlags[k] = flag.Lookup(k).Value.String()
		}
		logger.Printf("iniflags: read updated config. Modified flags are: %v", modifiedFlags)
		Generation++
		issueFlagChangeCallbacks(oldFlagValues)
	}
}

// FlagChangeCallback is called when the given flag is changed.
//
// The callback may be registered for any flag via OnFlagChange().
type FlagChangeCallback func()

// OnFlagChange registers the callback, which is called after the given flag
// value is initialized and/or changed.
//
// Flag values are initialized during iniflags.Parse() call.
// Flag value can be changed on config re-read after obtaining SIGHUP signal
// or if periodic config re-read is enabled with -configUpdateInterval flag.
//
// Note that flags set via command-line cannot be overriden via config file modifications.
func OnFlagChange(flagName string, callback FlagChangeCallback) {
	if parsed {
		verifyFlagChangeFlagName(flagName)
	}
	flagChangeCallbacks[flagName] = append(flagChangeCallbacks[flagName], callback)
}

func verifyFlagChangeFlagName(flagName string) {
	if flag.Lookup(flagName) == nil {
		logger.Fatalf("iniflags: cannot register FlagChangeCallback for non-existing flag [%s]", flagName)
	}
}

func issueFlagChangeCallbacks(oldFlagValues map[string]string) {
	for flagName := range oldFlagValues {
		if fs, ok := flagChangeCallbacks[flagName]; ok {
			for _, f := range fs {
				f()
			}
		}
	}
}

func issueAllFlagChangeCallbacks() {
	for _, fs := range flagChangeCallbacks {
		for _, f := range fs {
			f()
		}
	}
}

func sighupHandler(ch <-chan os.Signal) {
	for _ = range ch {
		updateConfig()
	}
}

func parseConfigFlags() (oldFlagValues map[string]string, ok bool) {
	configPath := *config
	if !strings.HasPrefix(configPath, "./") {
		if configPath, ok = combinePath(os.Args[0], *config); !ok {
			return nil, false
		}
	}
	if configPath == "" {
		return nil, true
	}
	parsedArgs, ok := getArgsFromConfig(configPath)
	if !ok {
		return nil, false
	}
	missingFlags := getMissingFlags()

	ok = true
	oldFlagValues = make(map[string]string)
	for _, arg := range parsedArgs {

		f := flag.Lookup(arg.Key)
		if f == nil {
			// Check if the key is a shorthand
			if fullName, isShorthand := flagShorthands[arg.Key]; isShorthand {
				f = flag.Lookup(fullName)
				arg.Key = fullName // Update the key to use the full name
			}
		}
		if f == nil {
			logger.Printf("iniflags: unknown flag name=[%s] found at line [%d] of file [%s]", arg.Key, arg.LineNum, arg.FilePath)
			if !*allowUnknownFlags {
				ok = false
			}
			continue
		}

		if _, found := missingFlags[f.Name]; found {
			oldValue := f.Value.String()
			if oldValue == arg.Value {
				continue
			}
			if err := f.Value.Set(arg.Value); err != nil {
				logger.Printf("iniflags: error when parsing flag [%s] value [%s] at line [%d] of file [%s]: [%s]", arg.Key, arg.Value, arg.LineNum, arg.FilePath, err)
				ok = false
				continue
			}
			if oldValue != f.Value.String() {
				oldFlagValues[arg.Key] = oldValue
			}
		}
	}

	if !ok {
		// restore old flag values
		for k, v := range oldFlagValues {
			flag.Set(k, v)
		}
		oldFlagValues = nil
	}

	return oldFlagValues, ok
}

func checkImportRecursion(configPath string) bool {
	for _, path := range importStack {
		if path == configPath {
			logger.Printf("iniflags: import recursion found for [%s]: %v", configPath, importStack)
			return false
		}
	}
	return true
}

type flagArg struct {
	Key      string
	Value    string
	FilePath string
	LineNum  int
	Comment  string
}

func stripBOM(s string) string {
	if len(s) < 3 {
		return s
	}
	bom := s[:3]
	if bom == "\ufeff" || bom == "\ufffe" {
		return s[3:]
	}
	return s
}

func ReadIniFile(iniFilePath string) (args []flagArg, ok bool) {
	return getArgsFromConfig(iniFilePath)
}

func getArgsFromConfig(configPath string) (args []flagArg, ok bool) {
	if !checkImportRecursion(configPath) {
		return nil, false
	}
	importStack = append(importStack, configPath)
	defer func() {
		importStack = importStack[:len(importStack)-1]
	}()

	file, err := openConfigFile(configPath)
	if err != nil {
		return nil, *allowMissingConfig
	}
	defer file.Close()
	r := bufio.NewReader(file)

	var lineNum int
	var comment = ""
	var multilineFA flagArg
	for {
		lineNum++
		line, err := r.ReadString('\n')

		if err != nil && line == "" {
			if err == io.EOF {
				if len(multilineFA.Key) > 0 {
					// flush the last multiline arg
					args = append(args, multilineFA)
				}
				break
			}
			logger.Printf("iniflags: error when reading file [%s] at line %d: [%s]", configPath, lineNum, err)
			return nil, false
		}

		// check if line is encoded in UTF-8
		if !utf8.ValidString(line) {
			logger.Printf("iniflags: invalid UTF-8 encoding at line %d of file [%s]", lineNum, configPath)
			return nil, false
		}

		if lineNum == 1 {
			line = stripBOM(line)
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#import ") {
			importPath, _, ok := unquoteValue(line[7:], lineNum, configPath)
			if !ok {
				return nil, false
			}
			if importPath, ok = combinePath(configPath, importPath); !ok {
				return nil, false
			}
			importArgs, ok := getArgsFromConfig(importPath)
			if !ok {
				return nil, false
			}
			args = append(args, importArgs...)
			continue
		}
		if line == "" || line[0] == '[' {
			comment = ""
			continue
		}
		if line[0] == '#' || line[0] == ';' {
			//save the comment and move to the next line
			comment = line[1:]
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			logger.Printf("iniflags: cannot split [%s] at line %d into key and value in config file [%s]", line, lineNum, configPath)
			return nil, false
		}
		key := strings.TrimSpace(parts[0])

		value, cmt, ok := unquoteValue(parts[1], lineNum, configPath)
		if !ok {
			return nil, false
		}
		if comment == "" {
			comment = cmt
		}

		fa := flagArg{
			Key:      key,
			Value:    value,
			FilePath: configPath,
			LineNum:  lineNum,
			Comment:  comment,
		}

		comment = ""
		if !strings.HasSuffix(key, "}") {
			if len(multilineFA.Key) > 0 {
				// flush the last multiline arg
				args = append(args, multilineFA)
				multilineFA = flagArg{}
			}

			args = append(args, fa)
			continue
		}

		// multiline arg
		n := strings.LastIndex(key, "{")
		if n < 0 {
			log.Printf("iniflags: cannot find '{' in the multiline key [%s] at line %d, file [%s]", key, lineNum, configPath)
			return nil, false
		}
		switch multilineFA.Key {
		case "":
			// the first line for multiline arg
			multilineFA = fa
			multilineFA.Key = key[:n]
		case key[:n]:
			// the subsequent line for multiline arg
			delimiter := key[n+1 : len(key)-1]
			multilineFA.Value += delimiter
			multilineFA.Value += value
		default:
			// new multiline arg
			args = append(args, multilineFA)
			multilineFA = fa
			multilineFA.Key = key[:n]
		}
	}

	return args, true
}

func openConfigFile(path string) (io.ReadCloser, error) {
	if isHTTP(path) {
		var resp *http.Response
		var err error
		// check path if it is secure
		if isSecure(path) {
			// It's a https path, so no need to check if unsecure is set
			resp, err = http.Get(path)
		} else {
			if !*unsecure {
				logger.Printf("iniflags: cannot load config file at [%s]: unsecure communication is not allowed", path)
				return nil, fmt.Errorf("unsecure communication is not allowed")
			} else {
				resp, err = http.Get(path)
				// warn if unsecure is set and the path is not secure
				logger.Printf("iniflags: unsecure communication with the server at [%s]", path)
			}
		}

		if err != nil {
			logger.Printf("iniflags: cannot load config file at [%s]: [%s]\n", path, err)
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			logger.Printf("iniflags: unexpected http status code when obtaining config file [%s]: %d. Expected %d", path, resp.StatusCode, http.StatusOK)
			return nil, err
		}
		return resp.Body, nil
	}

	file, err := os.Open(path)
	if err != nil {
		if !(*allowMissingConfig) {
			logger.Printf("iniflags: cannot open config file at [%s]: [%s]", path, err)
		}
		return nil, err
	}

	// // check if file is properly formatted UTF-8
	// if !utf8.ValidString(file) {
	// 	logger.Printf("iniflags: config file at [%s] is not properly formatted UTF-8", path)
	// 	return nil, err
	// }

	return file, nil
}

func combinePath(basePath, relPath string) (string, bool) {
	if isHTTP(basePath) {
		base, err := url.Parse(basePath)
		if err != nil {
			logger.Printf("iniflags: error when parsing http base path [%s]: %s", basePath, err)
			return "", false
		}
		rel, err := url.Parse(relPath)
		if err != nil {
			logger.Printf("iniflags: error when parsing http rel path [%s] for base [%s]: %s", relPath, basePath, err)
			return "", false
		}
		return base.ResolveReference(rel).String(), true
	}

	if relPath == "" || relPath[0] == '/' || isHTTP(relPath) {
		return relPath, true
	}
	return path.Join(path.Dir(basePath), relPath), true
}

func isHTTP(path string) bool {
	return strings.HasPrefix(strings.ToLower(path), "http://") || strings.HasPrefix(strings.ToLower(path), "https://")
}

func isSecure(path string) bool {
	return strings.HasPrefix(strings.ToLower(path), "https://")
}

func getMissingFlags() map[string]bool {
	setFlags := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		setFlags[f.Name] = true
	})

	missingFlags := make(map[string]bool)
	flag.VisitAll(func(f *flag.Flag) {
		if _, ok := setFlags[f.Name]; !ok {
			missingFlags[f.Name] = true
		}
	})
	return missingFlags
}

func dumpFlags() {
	flag.VisitAll(func(f *flag.Flag) {
		if _, exclude := flagsToExcludeFromDump[f.Name]; !exclude {
			fmt.Printf("%s = %s  # %s\n", f.Name, quoteValue(f.Value.String()), escapeUsage(f.Usage))
		}
	})
}

// escapeUsage escapes the usage string so it can be used as a comment in an ini file.
func escapeUsage(s string) string {
	// escape all the special characters that are not allowed. (tab, vertical tab, form feed, backspace, alert, backslash, double quote, superscript 2, superscript 3, superscript 1, superscript 0, superscript 4, superscript 5, superscript 6, superscript 7, superscript 8, superscript 9)
	stringsToReplace := []string{"\t", "\v", "\f", "\b", "\a", "\\", "\"", "\u00B2", "\u00B3", "\u00B9", "\u2070", "\u2074", "\u2075", "\u2076", "\u2077", "\u2078", "\u2079"}
	s = strings.Replace(s, "\n", "\n    # ", -1)
	for _, str := range stringsToReplace {
		s = strings.Replace(s, str, "", -1)
	}

	return s
}

func quoteValue(v string) string {
	if !strings.ContainsAny(v, "\n#;") && strings.TrimSpace(v) == v {
		return v
	}
	v = strings.Replace(v, "\\", "\\\\", -1)
	v = strings.Replace(v, "\n", "\\n", -1)
	v = strings.Replace(v, "\"", "\\\"", -1)
	return fmt.Sprintf("\"%s\"", v)
}

func unquoteValue(val string, lineNum int, configPath string) (string, string, bool) {
	v := strings.TrimSpace(val)
	if len(v) == 0 {
		return "", "", true
	}
	if v[0] != '"' {
		return removeTrailingComments(v), getTrailingComment(v), true
	}
	n := strings.LastIndex(v, "\"")
	if n == -1 {
		logger.Printf("iniflags: unclosed string found [%s] at line %d in config file [%s]", v, lineNum, configPath)
		return "", "", false
	}
	v = v[1:n]
	v = strings.Replace(v, "\\\"", "\"", -1)
	v = strings.Replace(v, "\\n", "\n", -1)
	v = strings.Replace(v, "\\\\", "\\", -1)

	logger.Printf("iniflags: unquoted value [%s]", v)

	//to get the comment remove the value from the original value and get the trailing comment
	comment := getTrailingComment(strings.Replace(val, fmt.Sprintf("%q", v), "", 1))
	logger.Printf("iniflags: comment [%s]", comment)
	return v, comment, true
}

func removeTrailingComments(v string) string {
	v = strings.Split(v, "#")[0]
	v = strings.Split(v, ";")[0]
	return strings.TrimSpace(v)
}

func getTrailingComment(v string) string {
	if len(v) == 0 {
		return ""
	}
	if v[0] == '"' {
		return ""
	}
	s := strings.Split(v, "#")
	if len(s) > 1 {
		return s[1]
	}
	s = strings.Split(v, ";")
	if len(s) > 1 {
		return s[1]
	}
	return ""
}

// SetConfigFile sets path to config file.
//
// Call this function before Parse() if you need default path to config file
// when -config command-line flag is not set.
func SetConfigFile(path string) {
	if parsed {
		logger.Panicf("iniflags: SetConfigFile() must be called before Parse()")
	}
	*config = path
}

func SetAllowMissingConfigFile(allowed bool) {
	if parsed {
		panic("iniflags: SetAllowMissingConfigFile() must be called before Parse()")
	}
	*allowMissingConfig = allowed
}

func SetAllowUnknownFlags(allowed bool) {
	if parsed {
		logger.Panicf("iniflags: SetAllowUnknownFlags() must be called before Parse()")
	}
	*allowUnknownFlags = allowed
}

func SetConfigUpdateInterval(interval time.Duration) {
	if parsed {
		logger.Panicf("iniflags: SetConfigUpdateInterval() must be called before Parse()")
	}
	*configUpdateInterval = interval
}

// RegisterShorthand registers a shorthand for a flag.
// The shorthand can be used in config files instead of the full flag name.
func RegisterShorthand(shorthand, fullName string) error {
	if parsed {
		return fmt.Errorf("iniflags: RegisterShorthand() must be called before Parse()")
	}

	if flag.Lookup(fullName) == nil {
		return fmt.Errorf("iniflags: cannot register shorthand [%s] for non-existing flag [%s]", shorthand, fullName)
	}

	if existing, exists := flagShorthands[shorthand]; exists {
		return fmt.Errorf("iniflags: shorthand [%s] already registered for flag [%s]", shorthand, existing)
	}
	// or if the shorthand is already know as full name for another flag
	if flag.Lookup(shorthand) != nil {
		return fmt.Errorf("iniflags: shorthand [%s] already registered as a flag name", shorthand)
	}

	flagShorthands[shorthand] = fullName
	return nil
}

// RegisterCommandLineShorthand registers a shorthand that can be used on the command line.
// The shorthand can be used both in config files and as a command-line flag.
func RegisterCommandLineShorthand(shorthand, fullName string) error {
	err := RegisterShorthand(shorthand, fullName)
	if err != nil {
		return err
	}

	commandLineShorthands[shorthand] = true
	return nil
}

// Logger is a slimmed-down version of the log.Logger interface, which only includes the methods we use.
// This interface is accepted by SetLogger() to redirect log output to another destination.
type Logger interface {
	Printf(format string, v ...interface{})
	Fatalf(format string, v ...interface{})
	Panicf(format string, v ...interface{})
}

// logger is the global Logger used to output log messages.  By default, it outputs to the same place and with the same
// format as the standard libary log package calls.  It can be changed via SetLogger().
var logger Logger = log.New(os.Stderr, "", log.LstdFlags)

func SetLogger(l Logger) {
	logger = l
}

// customUsage displays the standard flag usage message along with registered shorthands
func customUsage() {
	// First call the original usage function
	originalUsage()

	// Display shorthand information if any exist
	if len(flagShorthands) > 0 {
		// Create a map to collect all shorthands for each flag
		flagToShorthands := make(map[string][]string)

		// Group shorthands by their full flag name
		for short, full := range flagShorthands {
			flagToShorthands[full] = append(flagToShorthands[full], short)
		}

		// Only print the header if we have shorthands to show
		if len(flagToShorthands) > 0 {
			fmt.Fprintf(flag.CommandLine.Output(), "\nRegistered flag shorthands:\n")

			// Find the maximum length for alignment
			maxLen := 0
			for full := range flagToShorthands {
				if len(full) > maxLen {
					maxLen = len(full)
				}
			}

			// Sort the flags for consistent output
			flags := make([]string, 0, len(flagToShorthands))
			for full := range flagToShorthands {
				flags = append(flags, full)
			}
			sort.Strings(flags)

			// Print each flag with its shorthands
			for _, full := range flags {
				shorts := flagToShorthands[full]
				sort.Strings(shorts)

				shortList := strings.Join(shorts, ", ")
				cmdLine := ""
				for _, s := range shorts {
					if commandLineShorthands[s] {
						cmdLine = " (command-line)"
						break
					}
				}

				fmt.Fprintf(flag.CommandLine.Output(), "  -%s%*s -[%s]%s\n",
					full, maxLen-len(full)+1, "", shortList, cmdLine)
			}
		}
	}
}

// ExcludeFlagFromDump excludes the flag from the output of the dumpflags command.
// This is useful for sensitive flags that should not be exposed in the output.
func ExcludeFlagFromDump(flagName string) {
	flagsToExcludeFromDump[flagName] = true
}
