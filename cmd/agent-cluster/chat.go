package main

import (
	"time"

	"github.com/skinnyrad/agent-cluster/internal/chat"
	"github.com/skinnyrad/agent-cluster/internal/httputil"
)

func runChat(args []string) error {
	fs := newFlags("chat", "Talk to the cluster from the terminal.")
	url := fs.String("url", httputil.Getenv("CONTROLLER_URL", "http://localhost:8100"), "controller base URL [CONTROLLER_URL]")
	token := fs.String("token", httputil.Getenv("CLUSTER_TOKEN", ""), "cluster token, if the controller uses one [CLUSTER_TOKEN]")
	timeout := fs.Int("timeout", httputil.GetenvInt("CHAT_TIMEOUT", 900), "seconds to follow one job before giving up [CHAT_TIMEOUT]")
	fs.Parse(args)

	return chat.Run(chat.Config{
		ControllerURL: *url,
		Token:         *token,
		Timeout:       time.Duration(*timeout) * time.Second,
	})
}
