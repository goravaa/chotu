package main

import (
	"context"
	"log"

	"github.com/goravaa/chotu/chotu-setup/internals/config"
	"github.com/goravaa/chotu/chotu-setup/internals/setup"
	"github.com/goravaa/chotu/chotu-setup/internals/state"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatal(err)
	}

	configHash, err := config.Hash(cfg)
	if err != nil {
		log.Fatal(err)
	}

	db, err := state.Open("/var/lib/chotu/chotu.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	store := state.NewStore(db)

	machine, err := state.GetMachine(db)
	if err != nil {
		log.Fatal(err)
	}

	if machine == nil {
		if err := state.CreateMachine(db, configHash); err != nil {
			log.Fatal(err)
		}
	}

	tasks := setup.Tasks()
	runner := setup.NewRunner(tasks, store)

	results := runner.Run(context.Background())

	log.Printf("setup completed: %d tasks", len(results))
}
