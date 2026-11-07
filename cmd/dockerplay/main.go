package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/utkarsh2801/docker-workspaces/internal/compute"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: dockerplay create|get|delete <uid> | list")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	p, err := compute.NewDockerProvider()

	if err != nil {
		fail(err)
	}
	switch cmd := os.Args[1]; cmd {
	case "list":
		instances, err := p.List(ctx)
		if err != nil {
			fail(err)
		}
		for _, i := range instances {
			fmt.Printf("%s %s %s %s %s\n", i.ID, i.Name, i.UID, i.State, i.IP)
		}
	case "create", "get", "delete":
		if len(os.Args) < 3 {
			fail(fmt.Errorf("usage: dockerplay %s <uid>", cmd))
		}
		uid := os.Args[2]
		switch cmd {
		case "create":
			id, err := p.Create(ctx, compute.CreateRequest{
				UID:   uid,
				Owner: "me@example.com",
				Image: "traefik/whoami",
				Port:  80,
			})
			if err != nil {
				fail(err)
			}
			fmt.Println("created", id)
		case "get":
			inst, err := p.Get(ctx, uid)
			if err != nil {
				fail(err)
			}
			fmt.Printf("%s %s %s %s %s\n", inst.ID, inst.Name, inst.UID, inst.State, inst.IP)
		case "delete":
			err := p.Delete(ctx, uid)
			if err != nil {
				fail(err)
			}
			fmt.Println("deleted", uid)
		}
	default:
		fail(fmt.Errorf("unknown command: %s", cmd))
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
