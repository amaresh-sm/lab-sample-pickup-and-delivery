package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	mongostore "labgolden/internal/mongo"
)

func main() {
	drop := flag.Bool("drop", false, "drop the selected database before recreating the schema")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := mongostore.Open(ctx, mongostore.URI())
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	if *drop {
		err = store.Reset(ctx)
	} else {
		err = store.EnsureSchema(ctx)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "mongodb schema ready")
}
