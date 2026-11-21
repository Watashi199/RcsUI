package main

import (
	_ "github.com/rclone/rclone/backend/all"
	_ "github.com/rclone/rclone/fs/operations"
	_ "github.com/rclone/rclone/fs/sync"
	"github.com/rclone/rclone/librclone/librclone"
)

func main() { librclone.Initialize() }
