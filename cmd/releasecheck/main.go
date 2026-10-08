package main

import (
	"ddyidc/internal/update"
	"flag"
	"fmt"
	"os"
)

func main() {
	root := flag.String("root", ".", "release source or artifact root")
	artifact := flag.Bool("artifact", false, "validate public static artifacts")
	flag.Parse()
	if err := update.ValidateReleaseTree(*root, *artifact); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Release source/artifact policy passed")
}
