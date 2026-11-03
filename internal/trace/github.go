package trace

import (
	"strings"
)

// Command is a GitHub Actions workflow command that annotates a file, one
// line of a job's log that the runner turns into an annotation:
//
//	::error file=src/Foo.phn,line=12,col=5,title=P2103::Message
//
// Name is error, warning or notice. Property values and the message are held
// unescaped; String escapes them as the runner expects, the way
// @actions/core's issueCommand does: %, CR and LF in both, and : and , in
// property values.
type Command struct {
	Name    string
	Props   []Prop
	Message string
}

// Prop is one key=value of a command, in the order written.
type Prop struct {
	Key, Value string
}

// String is the command's line, without a line break.
func (c Command) String() string {
	var b strings.Builder
	b.WriteString("::" + c.Name)
	for i, p := range c.Props {
		if i == 0 {
			b.WriteByte(' ')
		} else {
			b.WriteByte(',')
		}
		b.WriteString(p.Key + "=" + escapeProperty(p.Value))
	}
	b.WriteString("::" + escapeData(c.Message))
	return b.String()
}

// Get is the value of the property key, "" and false when the command has
// none.
func (c Command) Get(key string) (string, bool) {
	for _, p := range c.Props {
		if p.Key == key {
			return p.Value, true
		}
	}
	return "", false
}

// ParseCommand reads an annotation command: a line that starts with
// ::error, ::warning or ::notice, the properties after a space, the message
// after the second ::.
func ParseCommand(line string) (Command, bool) {
	rest, ok := strings.CutPrefix(line, "::")
	if !ok {
		return Command{}, false
	}
	head, message, ok := strings.Cut(rest, "::")
	if !ok {
		return Command{}, false
	}
	name, props, _ := strings.Cut(head, " ")
	switch name {
	case "error", "warning", "notice":
	default:
		return Command{}, false
	}
	c := Command{Name: name, Message: unescape(message)}
	for _, kv := range strings.Split(props, ",") {
		if kv == "" {
			continue
		}
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return Command{}, false
		}
		c.Props = append(c.Props, Prop{Key: strings.TrimSpace(k), Value: unescape(v)})
	}
	return c, true
}

func escapeData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func escapeProperty(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}

// unescape undoes both escapes, %25 last, as the runner does.
func unescape(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	s = strings.NewReplacer("%0D", "\r", "%0A", "\n", "%3A", ":", "%2C", ",").Replace(s)
	return strings.ReplaceAll(s, "%25", "%")
}
