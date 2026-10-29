// Command chat is an interactive terminal client for the agent-cluster controller.
//
//	go run ./cmd/chat [--url http://localhost:8100]
//
// Slash commands:
//
//	/workers  show live worker status table
//	/clear    clear conversation history
//	/help     show available commands
//	/quit     exit
package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/peterh/liner"

	"github.com/skinnyrad/agent-cluster/internal/httputil"
)

func main() {
	defaultURL := httputil.Getenv("CONTROLLER_URL", "http://localhost:8100")
	url := flag.String("url", defaultURL, "controller base URL")
	flag.Parse()
	baseURL := strings.TrimRight(*url, "/")
	initMarkdownStyle()

	// liner provides up/down input recall and left/right cursor movement.
	line := liner.NewLiner()
	defer line.Close()
	line.SetCtrlCAborts(true)

	var hist history

	fmt.Println()
	fmt.Println(panel("",
		bold.Inherit(cyan).Render("Agent Cluster Chat")+"\n"+
			dim.Render("Controller:")+" "+yellow.Render(baseURL)+"\n"+
			dim.Render("Type a message to dispatch to the fleet.  Use ")+cyan.Render("/help")+dim.Render(" for commands."),
		lipgloss.Color("6")))
	fmt.Println()

	for {
		input, err := line.Prompt("You › ")
		if err != nil { // Ctrl-C, Ctrl-D or a closed terminal
			fmt.Println("\n" + dim.Render("Bye!"))
			return
		}
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		line.AppendHistory(input)

		// Slash commands
		if strings.HasPrefix(input, "/") {
			cmd := strings.ToLower(strings.Fields(input)[0])
			switch cmd {
			case "/quit", "/exit", "/q":
				fmt.Println(dim.Render("Bye!"))
				return
			case "/clear":
				hist.Clear()
				fmt.Println(green.Render("Conversation history cleared."))
			case "/workers":
				printWorkersTable(baseURL)
			case "/help":
				printHelp()
			default:
				fmt.Printf("%s %s  (type %s for list)\n", yellow.Render("Unknown command:"), cmd, cyan.Render("/help"))
			}
			continue
		}

		// Dispatch to fleet
		status := startStatus("Tasking fleet...")
		data, err := dispatchPrompt(baseURL, input, &hist, status)
		status.Stop()
		if err != nil {
			printDispatchError(baseURL, err)
			continue
		}
		printDispatchResult(data)

		// Keep this interaction in the rolling history.
		if data.Synthesis != "" {
			hist.Add("user", input)
			hist.Add("assistant", data.Synthesis)
		}
	}
}

func printDispatchError(baseURL string, err error) {
	errRed := bold.Inherit(red)
	var statusErr *httpStatusError
	var opErr *net.OpError
	switch {
	case errors.As(err, &statusErr):
		fmt.Printf("%s %s\n", errRed.Render(fmt.Sprintf("HTTP %d:", statusErr.Code)), statusErr.Body)
	case errors.As(err, &opErr) && opErr.Op == "dial":
		fmt.Printf("%s Is the controller running at %s?\n", errRed.Render("Connection refused."), yellow.Render(baseURL))
	default:
		fmt.Printf("%s %v\n", errRed.Render("Error:"), err)
	}
}
