package main

import "flag"

// permute reorders args so flags may appear after positional arguments.
// Go's flag package stops parsing at the first non-flag word, which makes the
// natural `cfdo create my-do -force` silently drop the flag.
func permute(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)

		// A flag written as -name=value carries its own value.
		name := a[1:]
		if len(name) > 0 && name[0] == '-' {
			name = name[1:]
		}
		for j := 0; j < len(name); j++ {
			if name[j] == '=' {
				name = name[:j]
				goto next
			}
		}
		// Otherwise a non-boolean flag consumes the following word.
		if f := fs.Lookup(name); f != nil && !isBoolFlag(f) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	next:
	}
	return append(flags, positional...)
}

func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}
