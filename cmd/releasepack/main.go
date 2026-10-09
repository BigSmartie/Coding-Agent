package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/BigSmartie/Coding-Agent/internal/releasepack"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("usage: releasepack pack|sbom|checksums [flags]")
	}
	command := argv[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	version := flags.String("version", "", "release tag")
	epoch := flags.Int64("epoch", 0, "source date epoch")
	out := flags.String("out", "dist", "output directory or SBOM path")
	binary := flags.String("binary", "", "binary path")
	goos := flags.String("os", "", "target OS")
	goarch := flags.String("arch", "", "target architecture")
	if err := flags.Parse(argv[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	switch command {
	case "pack":
		path, err := releasepack.Archive(*binary, *out, *version, *goos, *goarch, *epoch)
		if err == nil {
			fmt.Println(path)
		}
		return err
	case "sbom":
		return releasepack.WriteSBOM(*out, *version, *epoch)
	case "checksums":
		path, err := releasepack.Checksums(*out)
		if err == nil {
			fmt.Println(path)
		}
		return err
	default:
		return fmt.Errorf("unknown releasepack command %q (%s)", command, strconv.Quote(argv[0]))
	}
}
