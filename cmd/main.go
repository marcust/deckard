package main

import (
	"github.com/Ragnaroek/deckard"
)

func main() {
	config, err := deckard.LoadConfig()
	if err != nil {
		panic(err)
	}

	ui, err := deckard.BuildUI(config)
	if err != nil {
		panic(err)
	}

	// update UI from DB first (much faster than a repo update)
	err = deckard.UpdateFromDB(ui)
	if err != nil {
		panic(err)
	}
	deckard.UpdateFromRepo(ui)

	err = ui.Run()
	if err != nil {
		panic(err)
	}
}
