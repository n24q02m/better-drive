package main

import (
	"fmt"
	"os"

	_ "github.com/rclone/rclone/backend/drive" // register CHỈ Drive backend (prune 50+)
	"github.com/rclone/rclone/librclone/librclone"
)

func main() {
	librclone.Initialize()
	defer librclone.Finalize()
	out, status := librclone.RPC("core/version", "{}")
	if status != 200 {
		fmt.Fprintf(os.Stderr, "RPC failed status=%d out=%s\n", status, out)
		os.Exit(1)
	}
	fmt.Printf("librclone OK status=%d\n%s\n", status, out)
}
