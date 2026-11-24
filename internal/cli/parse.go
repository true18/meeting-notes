// Package cli parses commands and renders user-facing output.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Command is a parsed CLI request.
type Command struct {
	Name      string
	User      string
	Arguments []string
	MeetingID int64
}

// Parse parses global options and a command without executing business logic.
func Parse(args []string) (Command, error) {
	flags := flag.NewFlagSet("meetings", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	user := flags.String("user", "", "CLI user identifier")
	if err := flags.Parse(args); err != nil {
		return Command{}, err
	}
	remaining := flags.Args()
	if len(remaining) == 0 {
		return Command{}, errors.New("command is required")
	}
	name := remaining[0]
	known := map[string]bool{"start": true, "load": true, "list": true, "status": true, "get": true, "summary": true, "find": true, "chat": true, "retry": true, "worker": true, "migrate": true}
	if !known[name] {
		return Command{}, fmt.Errorf("unknown command %q", name)
	}
	if name != "worker" && name != "migrate" && strings.TrimSpace(*user) == "" {
		return Command{}, errors.New("-user is required")
	}
	command := Command{Name: name, User: strings.TrimSpace(*user), Arguments: remaining[1:]}
	if name == "status" || name == "get" || name == "summary" || name == "retry" {
		if len(command.Arguments) != 1 {
			return Command{}, fmt.Errorf("%s requires one meeting ID", name)
		}
		id, err := strconv.ParseInt(command.Arguments[0], 10, 64)
		if err != nil || id < 1 {
			return Command{}, fmt.Errorf("invalid meeting ID %q", command.Arguments[0])
		}
		command.MeetingID = id
	}
	return command, nil
}
