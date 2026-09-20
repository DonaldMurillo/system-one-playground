// Command sos runs the SysOneScript CLI. See cli.go for the implementation.
package main

import "os"

func main() {
	os.Exit(RunCLI(os.Args[1:], os.Stdout, os.Stderr))
}
