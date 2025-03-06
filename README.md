Hybrid configuration library
============================

Combine standard go flags with ini files.

Usage:

```bash

go get -u -a github.com/boomhut/iniflags
```

main.go
```go
package main

import (
	"flag"
	...
	"github.com/boomhut/iniflags"
	...
)

var (
	flag1 = flag.String("flag1", "default1", "Description1")
	...
	flagN = flag.Int("flagN", 123, "DescriptionN")
)

func main() {
	iniflags.Parse()  // use instead of flag.Parse()
}
```

dev.ini

```ini
    # comment1
    flag1 = "val1"  # comment2

    ...
    [section]
    flagN = 4  # comment3

    multilineFlag{,} = line1
    multilineFlag{,} = line2
    multilineFlag{|} = line3
    multilineFlag{} = line4
    # Now the multilineFlag equals to "line1,line2|line3line4"
```

```bash

# Run the app with flags set via command-line
go run main.go -config dev.ini -flagX=foobar

# Run compiled app with flags set via command-line
/path/to/app -config dev.ini -flagX=foobar

```

Now all unset flags obtain their value from .ini file provided in -config path.
If value is not found in the .ini, flag will retain its' default value.

Flag value priority:
  - value set via command-line
  - value from ini file
  - default value

Iniflags is compatible with real .ini config files with [sections] and #comments.
Sections and comments are skipped during config file parsing.

Iniflags can #import another ini files. For example,

base.ini
```ini
flag1 = value1
flag2 = value2
```

dev.ini
```ini
# import "base.ini"
# Now flag1="value1", flag2="value2"

flag2 = foobar
# Now flag1="value1", while flag2="foobar"
```

Both -config path and imported ini files can be addressed via http
or https links:

```bash
/path/to/app -config=https://google.com/path/to/config.ini
```

config.ini
```ini
# The following line will import configs from the given http link.
# import "http://google.com/path/to/config.ini"
```

All flags defined in the app can be dumped into stdout with ini-compatible sytax
by passing -dumpflags flag to the app. The following command creates ini-file 
with all the flags defined in the app:

```bash
/path/to/the/app -dumpflags > initial-config.ini
```


Iniflags also supports two types of online config reload:

  * Via SIGHUP signal:

```bash
kill -s SIGHUP <app_pid>
```

  * Via -configUpdateInterval flag. The following line will re-read config every 5 seconds:

```bash
/path/to/app -config=/path/to/config.ini -configUpdateInterval=5s
```


Advanced usage.

```go
package main

import (
	"flag"
	"iniflags"
	"log"
)

var listenPort = flag.Int("listenPort", 1234, "Port to listen to")

func init() {
	iniflags.OnFlagChange("listenPort", func() {
		startServerOnPort(*listenPort)
	})
}

func main() {
	// iniflags.Parse() starts the server on the -listenPort via OnFlagChange()
	// callback registered above.
	iniflags.Parse()
}
```

# iniflags

A Go package that extends the standard `flag` package to support reading flags from INI config files.

## Installation

```bash
go get github.com/yourusername/iniflags
```

## Basic Usage

1. Import the package in your Go code:
```go
import (
    "flag"
    "github.com/yourusername/iniflags"
)
```

2. Define flags using the standard flag package:
```go
var (
    addr = flag.String("addr", ":8080", "TCP address to listen to")
    dbPath = flag.String("dbPath", "/tmp/mydb", "Path to the database directory")
)
```

3. Call `iniflags.Parse()` instead of `flag.Parse()`:
```go
func main() {
    iniflags.Parse()
    
    // Now use flags as usual
    fmt.Printf("addr=%s, dbPath=%s\n", *addr, *dbPath)
}
```

## Config File Format

The config file uses INI format:

```ini
# This is a comment
addr = :9090
dbPath = /var/db/myapp

#import /etc/myapp/common.ini
```

## Command Line Options

- `-config=/path/to/config.ini`: Specify the path to the config file
- `-configUpdateInterval=10s`: Automatically reload config file every 10 seconds
- `-dumpflags`: Print all flags with their values in INI format
- `-allowMissingConfig`: Don't terminate if the config file is missing
- `-allowUnknownFlags`: Don't terminate if the config file contains unknown flags

## Features

1. **Automatic Config Reloading**: Reload configuration when `-configUpdateInterval` is set or when SIGHUP is received
2. **Flag Change Callbacks**: Register callbacks to be called when flag values change
3. **Import Directive**: Include other config files using `#import` directive
4. **HTTP Support**: Load config files from HTTP/HTTPS URLs
5. **Shortcuts**: Register shorthand names for flags

## Advanced Usage

### Flag change notifications

```go
iniflags.OnFlagChange("addr", func() {
    // This will be called when addr flag value is initialized and/or changed
    fmt.Printf("addr changed to %s\n", *addr)
})
```

### Setting default config file

```go
// Must be called before iniflags.Parse()
iniflags.SetConfigFile("/etc/myapp/default.ini")
```

### Registering flag shorthands

```go
// Register "a" as shorthand for "addr"
iniflags.RegisterShorthand("a", "addr")
```

// Registering flag shorthands

```go
// Register "a" as shorthand for "addr" (for config files only)
iniflags.RegisterShorthand("a", "addr")

// Register "v" as shorthand for "version" (for both config files and command line)
iniflags.RegisterCommandLineShorthand("v", "version")
```

With the command line shorthand registered, you can use it in your commands:

```bash
# These two are equivalent:
/path/to/app -version=1.0.0
/path/to/app -v=1.0.0
```
