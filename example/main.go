package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/boomhut/iniflags"
)

var (
	addr        = flag.String("addr", ":8080", "TCP address to listen to")
	dbPath      = flag.String("dbPath", "/tmp/mydb", "Path to the database directory")
	logLevel    = flag.String("logLevel", "info", "Logging level: debug, info, warn, error")
	cacheSize   = flag.Int("cacheSize", 1000, "Maximum number of items in cache")
	timeout     = flag.Duration("timeout", 5*time.Second, "Connection timeout")
	enableDebug = flag.Bool("debug", false, "Enable debug mode")
	version     = flag.String("version", "1.0.0", "Application version")
)

func main() {
	// Register a shorthand for config files only
	iniflags.RegisterShorthand("l", "logLevel")

	// Register a shorthand for both config files and command line
	iniflags.RegisterCommandLineShorthand("v", "version")

	// Register callbacks for flag changes
	iniflags.OnFlagChange("addr", func() {
		fmt.Printf("Address changed to %s\n", *addr)
	})

	// Parse flags from command-line and config file
	iniflags.Parse()

	// Now use flags as usual
	fmt.Printf("Configuration:\n")
	fmt.Printf("  addr: %s\n", *addr)
	fmt.Printf("  dbPath: %s\n", *dbPath)
	fmt.Printf("  logLevel: %s\n", *logLevel)
	fmt.Printf("  cacheSize: %d\n", *cacheSize)
	fmt.Printf("  timeout: %s\n", *timeout)
	fmt.Printf("  debug: %v\n", *enableDebug)
	fmt.Printf("  version: %s\n", *version)

	if *enableDebug {
		log.Println("Debug mode enabled")
	}

	// Your application logic here
	select {}
}
