package main

import (
	"git.duckfam.us/jj-software/cog/plugin/plugcmd"
	"git.duckfam.us/jj-software/cog/plugin/recipes"
)

func main() {
	plugcmd.Run(recipes.New())
}
