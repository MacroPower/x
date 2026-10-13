package jsonschema

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
	"go.jacobcolvin.com/x/jsonschema/internal/tagparse"
)

// deprecatedPrefix opens the paragraph of a Go doc comment that marks its
// declaration deprecated, as the Go tools read it.
const deprecatedPrefix = "Deprecated:"

// deprecation is the deprecation that one source states for a field: a
// paragraph of a doc comment that opens with the deprecated prefix, the
// jsonschema tag, or a chain of embedded fields.
type deprecation struct {
	// The message is the text of the deprecationMessage keyword, or "" for a
	// deprecation without one.
	message string
	// The stated flag reports whether the source says anything about the
	// deprecation. A source that states it with deprecated unset keeps the
	// field current.
	stated     bool
	deprecated bool
}

// then returns d with later applied over it, as a tag applies over the doc
// comment of its field. A later deprecated=true keeps the message of d, and
// a later deprecated=false removes the message along with the mark.
func (d deprecation) then(later deprecation) deprecation {
	if !later.stated {
		return d
	}

	if later.deprecated && later.message == "" && d.deprecated {
		later.message = d.message
	}

	return later
}

// tagDeprecation returns the deprecation that a parsed jsonschema tag
// states.
func tagDeprecation(td tagparse.Deprecation) deprecation {
	return deprecation{message: td.Message, stated: td.Stated, deprecated: td.Deprecated}
}

// commentDeprecation returns the deprecation that the Deprecated: paragraph
// of doc states, with each line break of the paragraph replaced by a space.
// A doc with no such paragraph states nothing, and the first paragraph wins
// over a later one.
func commentDeprecation(doc string) deprecation {
	var para []string

	flush := func() (deprecation, bool) {
		if len(para) == 0 {
			return deprecation{}, false
		}

		text := strings.Join(para, " ")
		para = para[:0]

		rest, ok := strings.CutPrefix(text, deprecatedPrefix)
		if !ok || (rest != "" && rest[0] != ' ') {
			return deprecation{}, false
		}

		return deprecation{message: strings.TrimSpace(rest), stated: true, deprecated: true}, true
	}

	for line := range strings.Lines(doc) {
		line = strings.TrimSpace(line)
		if line != "" {
			para = append(para, line)

			continue
		}

		if d, ok := flush(); ok {
			return d
		}
	}

	d, _ := flush()

	return d
}

// settleFieldDeprecation decides the deprecation of the field p of the
// struct root, after its description and its tag ran. The Deprecated:
// paragraph of the doc comment comes first and the tag applies over it.
// A field that states nothing of its own takes the deprecation of the
// embedded fields that promote it. The decision lands on the field's
// canvas, and its message on the field node, for [node.markDeprecation].
func (g *run) settleFieldDeprecation(
	root reflect.Type,
	p *nodeProp,
	parent *Schema,
	comment string,
	tagged tagparse.Deprecation,
	embeds map[string]deprecation,
) error {
	own := commentDeprecation(comment).then(tagDeprecation(tagged))

	if !own.stated {
		path := p.fi.StructField.Index

		inherited, err := g.chainDeprecation(root, path[:max(len(path)-1, 0)], parent, embeds)
		if err != nil {
			return err
		}

		own = inherited
	}

	if !own.stated {
		return nil
	}

	p.schema.authored.Deprecated = own.deprecated
	p.schema.deprecationMessage = own.message

	return nil
}

// settleGhostMarks decides the deprecation of each name a composed embed
// won on the object node obj: the deprecation of that embed and of each
// embedded field above it, which the placeholder property of the name takes
// at render.
func (g *run) settleGhostMarks(obj *node, parent *Schema, embeds map[string]deprecation) error {
	obj.ghostMarks = nil

	if len(obj.ghostWon) == 0 || obj.typ == nil {
		return nil
	}

	// The field set resolved without error when the object was built, and
	// the walk is deterministic, so a second resolution names the same
	// embeds.
	resolved, _ := g.fields.Of(obj.typ) //nolint:errcheck // See the comment above.

	for _, name := range obj.ghostWon {
		path, ok := resolved.GhostEmbeds[name]
		if !ok {
			continue
		}

		d, err := g.chainDeprecation(obj.typ, path, parent, embeds)
		if err != nil {
			return err
		}

		if !d.deprecated {
			continue
		}

		if obj.ghostMarks == nil {
			obj.ghostMarks = map[string]deprecation{}
		}

		obj.ghostMarks[name] = d
	}

	return nil
}

// chainDeprecation returns the deprecation that the embedded fields along
// path, from the struct root down to the field path names, give the fields
// they promote. An embedded field that states a deprecation replaces the
// one the fields above it gave, so the nearest one wins. Each embedded
// field's deprecation stays in the embeds cache under its path.
func (g *run) chainDeprecation(
	root reflect.Type,
	path []int,
	parent *Schema,
	embeds map[string]deprecation,
) (deprecation, error) {
	var acc deprecation

	for i := 1; i <= len(path); i++ {
		key := fmt.Sprint(path[:i])

		d, ok := embeds[key]
		if !ok {
			var err error

			d, err = g.embedDeprecation(root, path[:i], parent)
			if err != nil {
				return deprecation{}, err
			}

			embeds[key] = d
		}

		if d.stated {
			acc = d
		}
	}

	return acc, nil
}

// embedDeprecation returns the deprecation that the embedded field at path
// in the struct root states for itself, from the Deprecated: paragraph of
// its doc comment and its jsonschema tag. The description provider receives
// the field with an empty Name, because an embedded field that promotes its
// fields has no JSON name.
func (g *run) embedDeprecation(root reflect.Type, path []int, parent *Schema) (deprecation, error) {
	sf := root.FieldByIndex(path)
	sf.Index = slices.Clone(path)

	var d deprecation

	if g.descriptionProvider != nil {
		fc := FieldContext{
			Type:        sf.Type,
			Owner:       reflectkind.DeclaringType(root, sf),
			Canvas:      &Schema{},
			Base:        &Schema{},
			Parent:      parent,
			StructField: sf,
			Draft:       g.draft,
		}

		doc, err := g.descriptionProvider.FieldDescription(g.ctx, fc)
		if err != nil {
			return deprecation{}, fmt.Errorf("describe embedded field %s of %s: %w", sf.Name, root, err)
		}

		d = commentDeprecation(doc)
	}

	tag, ok := sf.Tag.Lookup("jsonschema")
	if !ok {
		return d, nil
	}

	td, err := tagparse.ParseDeprecation(tag)
	if err != nil {
		return deprecation{}, fmt.Errorf("embedded field %s: %w", sf.Name, err)
	}

	return d.then(tagDeprecation(td)), nil
}

// markDeprecation writes the deprecation message of the field node n beside
// the deprecated keyword its canvas authored: on wrapper when the null split
// moved the keyword there, and on value otherwise. A nil wrapper stands for
// a field with no split.
func (n *node) markDeprecation(value, wrapper *Schema) {
	if n.deprecationMessage == "" || n.authored == nil || !n.authored.Deprecated {
		return
	}

	target := value
	if wrapper != nil && wrapper.Deprecated {
		target = wrapper
	}

	setDeprecationMessage(target, n.deprecationMessage)
}

// markGhosts gives each placeholder property of base, the rendered object
// node n, the deprecation that [run.settleGhostMarks] decided for its name.
// A property with a deprecation of its own keeps it. A copy replaces the
// property, because a hook might have aliased the literal into another
// slot.
func (n *node) markGhosts(base *Schema) {
	for _, name := range slices.Sorted(maps.Keys(n.ghostMarks)) {
		p := base.Properties[name]
		if p == nil || p.Deprecated {
			continue
		}

		cp := *p
		cp.Deprecated = true

		if msg := n.ghostMarks[name].message; msg != "" {
			setDeprecationMessage(&cp, msg)
		}

		base.Properties[name] = &cp
	}
}

// setDeprecationMessage sets the deprecationMessage keyword of s to msg, on
// a copy of its Extra map, which another schema might share.
func setDeprecationMessage(s *Schema, msg string) {
	extra := make(map[string]any, len(s.Extra)+1)
	maps.Copy(extra, s.Extra)

	extra[keyword.DeprecationMessage] = msg
	s.Extra = extra
}
