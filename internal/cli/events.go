package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/events"
	"github.com/mjrusso/voom/internal/host"
)

func eventsCommand() *cobra.Command {
	var sinceValue, untilValue string
	var filterValues []string
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Stream VM and forward change events",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			now := time.Now()
			req := events.Request{}
			if sinceValue != "" {
				if moment, err := events.ParseMoment(sinceValue, now); err == nil {
					req.Since = &events.Since{Time: &moment}
				} else if events.IsID(sinceValue) {
					req.Since = &events.Since{ID: sinceValue}
				} else {
					return fmt.Errorf("invalid --since value %q", sinceValue)
				}
			}
			if untilValue != "" {
				moment, err := parseUntil(untilValue, now)
				if err != nil {
					return err
				}
				req.Until = &moment
			}
			filters, err := parseEventFilters(filterValues)
			if err != nil {
				return err
			}
			reader := events.NewReader(host.ResolvePaths().Cache)
			return reader.Stream(cmd.Context(), req, func(ev events.Event) error {
				if !filters.match(ev) {
					return nil
				}
				if outputFormat(cmd) == "json" {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(ev)
				}
				_, err := fmt.Fprintln(cmd.OutOrStdout(), formatEvent(ev))
				return err
			})
		},
	}
	cmd.Flags().StringVar(&sinceValue, "since", "", "show events since a timestamp, duration, or event ID")
	cmd.Flags().StringVar(&untilValue, "until", "", "stream until a timestamp or duration")
	cmd.Flags().StringArrayVar(&filterValues, "filter", nil, "filter events by type, event, vm, or id")
	return cmd
}

func formatEvent(ev events.Event) string {
	name := ev.Actor.Attributes["vm"]
	if name == "" {
		name = ev.Actor.Attributes["name"]
	}
	base := fmt.Sprintf("%s %s %s %s", ev.Time, ev.Type, ev.Action, name)
	if ev.Type != "forward" {
		return strings.TrimSpace(base)
	}
	attrs := ev.Actor.Attributes
	return fmt.Sprintf("%s %s:%s -> %s (%s)", base, attrs["bind"], attrs["hostPort"], attrs["guestPort"], attrs["kind"])
}

func parseUntil(value string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(value); err == nil {
		return now.Add(d), nil
	}
	return events.ParseMoment(value, now)
}

type eventFilters map[string]map[string]struct{}

func parseEventFilters(values []string) (eventFilters, error) {
	out := eventFilters{}
	for _, value := range values {
		key, wanted, ok := strings.Cut(value, "=")
		if !ok || wanted == "" {
			return nil, fmt.Errorf("invalid event filter %q; use key=value", value)
		}
		switch key {
		case "type", "event", "vm", "id":
		default:
			return nil, fmt.Errorf("unsupported event filter %q", key)
		}
		if out[key] == nil {
			out[key] = map[string]struct{}{}
		}
		out[key][wanted] = struct{}{}
	}
	return out, nil
}

func (f eventFilters) match(ev events.Event) bool {
	values := map[string]string{"type": ev.Type, "event": ev.Action, "vm": ev.Actor.Attributes["vm"], "id": ev.Actor.ID}
	if values["vm"] == "" {
		values["vm"] = ev.Actor.Attributes["name"]
	}
	for key, wanted := range f {
		if _, ok := wanted[values[key]]; !ok {
			return false
		}
	}
	return true
}
