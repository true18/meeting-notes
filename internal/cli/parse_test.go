package cli

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    Command
		wantErr bool
	}{
		{name: "list", args: []string{"-user", "alice", "list"}, want: Command{Name: "list", User: "alice"}},
		{name: "worker", args: []string{"worker"}, want: Command{Name: "worker"}},
		{name: "status", args: []string{"-user", "alice", "status", "42"}, want: Command{Name: "status", User: "alice", Arguments: []string{"42"}, MeetingID: 42}},
		{name: "missing command", wantErr: true},
		{name: "missing user", args: []string{"list"}, wantErr: true},
		{name: "unknown", args: []string{"-user", "alice", "remove"}, wantErr: true},
		{name: "bad id", args: []string{"-user", "alice", "get", "bad"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse error = %v", err)
			}
			if err == nil && (got.Name != tt.want.Name || got.User != tt.want.User || got.MeetingID != tt.want.MeetingID) {
				t.Fatalf("Parse = %+v, want %+v", got, tt.want)
			}
		})
	}
}
