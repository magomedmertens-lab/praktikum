package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type ValError struct {
	File string
	Line int // 0 => line unknown / not required
	Msg  string
}

func (e ValError) Error() string { return e.Msg }

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: yamlvalid <path-to-yaml>")
		os.Exit(2)
	}
	path := os.Args[1]

	content, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read file content: %v\n", err)
		os.Exit(1)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		fmt.Fprintf(os.Stderr, "cannot unmarshal file content: %v\n", err)
		os.Exit(1)
	}

	errs := validatePod(path, &root)
	if len(errs) > 0 {
		for _, e := range errs {
			if e.Line > 0 {
				fmt.Fprintf(os.Stderr, "%s:%d %s\n", e.File, e.Line, e.Msg)
			} else {
				fmt.Fprintf(os.Stderr, "%s\n", e.Msg)
			}
		}
		os.Exit(1)
	}

	os.Exit(0)
}

func validatePod(file string, root *yaml.Node) []ValError {
	file = filepath.Clean(file)

	doc := firstDocument(root)
	if doc == nil {
		return []ValError{{File: file, Msg: "cannot unmarshal file content: empty document"}}
	}
	if doc.Kind != yaml.MappingNode {
		return []ValError{ve(file, doc.Line, "root must be object")}
	}

	var errs []ValError

	// Required top-level fields
	apiV := mustGet(doc, "apiVersion")
	kind := mustGet(doc, "kind")
	meta := mustGet(doc, "metadata")
	spec := mustGet(doc, "spec")

	if apiV == nil {
		errs = append(errs, ve(file, 0, "apiVersion is required"))
	} else {
		errs = append(errs, validateStringEquals(file, apiV, "apiVersion", []string{"v1"})...)
	}

	if kind == nil {
		errs = append(errs, ve(file, 0, "kind is required"))
	} else {
		errs = append(errs, validateStringEquals(file, kind, "kind", []string{"Pod"})...)
	}

	if meta == nil {
		errs = append(errs, ve(file, 0, "metadata is required"))
	} else {
		errs = append(errs, validateObjectMeta(file, meta)...)
	}

	if spec == nil {
		errs = append(errs, ve(file, 0, "spec is required"))
	} else {
		errs = append(errs, validatePodSpec(file, spec)...)
	}

	return errs
}

func validateObjectMeta(file string, n *yaml.Node) []ValError {
	var errs []ValError
	if n.Kind != yaml.MappingNode {
		return []ValError{ve(file, n.Line, "metadata must be object")}
	}

	name := mustGet(n, "name")
	if name == nil {
		errs = append(errs, ve(file, 0, "metadata.name is required"))
	} else {
		errs = append(errs, validateScalarType(file, name, "metadata.name", "string")...)
	}

	// namespace optional
	if ns := get(n, "namespace"); ns != nil {
		errs = append(errs, validateScalarType(file, ns, "metadata.namespace", "string")...)
	}

	// labels optional: object of string->string
	if labels := get(n, "labels"); labels != nil {
		if labels.Kind != yaml.MappingNode {
			errs = append(errs, ve(file, labels.Line, "metadata.labels must be object"))
		} else {
			for i := 0; i < len(labels.Content); i += 2 {
				k := labels.Content[i]
				v := labels.Content[i+1]
				if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
					errs = append(errs, ve(file, k.Line, "metadata.labels key must be string"))
				}
				if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
					errs = append(errs, ve(file, v.Line, "metadata.labels value must be string"))
				}
			}
		}
	}

	return errs
}

func validatePodSpec(file string, n *yaml.Node) []ValError {
	var errs []ValError
	if n.Kind != yaml.MappingNode {
		return []ValError{ve(file, n.Line, "spec must be object")}
	}

	// os optional (support both forms: scalar "linux"/"windows" OR object {name: linux})
	if osNode := get(n, "os"); osNode != nil {
		errs = append(errs, validatePodOS(file, osNode)...)
	}

	containers := mustGet(n, "containers")
	if containers == nil {
		errs = append(errs, ve(file, 0, "spec.containers is required"))
		return errs
	}

	errs = append(errs, validateContainers(file, containers)...)
	return errs
}

func validatePodOS(file string, n *yaml.Node) []ValError {
	allowed := map[string]bool{"linux": true, "windows": true}

	// Scalar form: os: linux
	if n.Kind == yaml.ScalarNode {
		if n.Tag != "!!str" {
			return []ValError{ve(file, n.Line, "os must be string")}
		}
		val := strings.TrimSpace(n.Value)
		if !allowed[val] {
			return []ValError{ve(file, n.Line, fmt.Sprintf("os has unsupported value '%s'", n.Value))}
		}
		return nil
	}

	// Object form: os: { name: linux }
	if n.Kind != yaml.MappingNode {
		return []ValError{ve(file, n.Line, "os must be object")}
	}

	name := mustGet(n, "name")
	if name == nil {
		return []ValError{ve(file, 0, "os.name is required")}
	}
	if name.Kind != yaml.ScalarNode || name.Tag != "!!str" {
		return []ValError{ve(file, name.Line, "os.name must be string")}
	}
	val := strings.TrimSpace(name.Value)
	if !allowed[val] {
		return []ValError{ve(file, name.Line, fmt.Sprintf("os.name has unsupported value '%s'", name.Value))}
	}
	return nil
}

func validateContainers(file string, n *yaml.Node) []ValError {
	if n.Kind != yaml.SequenceNode {
		return []ValError{ve(file, n.Line, "spec.containers must be array")}
	}
	if len(n.Content) == 0 {
		return []ValError{ve(file, n.Line, "spec.containers is required")}
	}

	var errs []ValError
	seenNames := map[string]bool{}
	nameRe := regexp.MustCompile(`^[a-z]+(_[a-z0-9]+)*$`)

	for idx, c := range n.Content {
		if c.Kind != yaml.MappingNode {
			errs = append(errs, ve(file, c.Line, fmt.Sprintf("spec.containers[%d] must be object", idx)))
			continue
		}

		// name required
		name := mustGet(c, "name")
		if name == nil {
			errs = append(errs, ve(file, 0, fmt.Sprintf("spec.containers[%d].name is required", idx)))
		} else if name.Kind != yaml.ScalarNode || name.Tag != "!!str" {
			errs = append(errs, ve(file, name.Line, "containers.name must be string"))
		} else {
			if !nameRe.MatchString(name.Value) {
				errs = append(errs, ve(file, name.Line, fmt.Sprintf("containers.name has invalid format '%s'", name.Value)))
			}
			if seenNames[name.Value] {
				errs = append(errs, ve(file, name.Line, fmt.Sprintf("containers.name has invalid format '%s'", name.Value)))
			}
			seenNames[name.Value] = true
		}

		// image required
		image := mustGet(c, "image")
		if image == nil {
			errs = append(errs, ve(file, 0, fmt.Sprintf("spec.containers[%d].image is required", idx)))
		} else if image.Kind != yaml.ScalarNode || image.Tag != "!!str" {
			errs = append(errs, ve(file, image.Line, "containers.image must be string"))
		} else {
			if !validImage(image.Value) {
				errs = append(errs, ve(file, image.Line, fmt.Sprintf("containers.image has invalid format '%s'", image.Value)))
			}
		}

		// ports optional
		if ports := get(c, "ports"); ports != nil {
			errs = append(errs, validatePorts(file, ports)...)
		}

		// probes optional
		if rp := get(c, "readinessProbe"); rp != nil {
			errs = append(errs, validateProbe(file, rp, "readinessProbe")...)
		}
		if lp := get(c, "livenessProbe"); lp != nil {
			errs = append(errs, validateProbe(file, lp, "livenessProbe")...)
		}

		// resources required
		res := mustGet(c, "resources")
		if res == nil {
			errs = append(errs, ve(file, 0, fmt.Sprintf("spec.containers[%d].resources is required", idx)))
		} else {
			errs = append(errs, validateResources(file, res)...)
		}
	}

	return errs
}

func validatePorts(file string, n *yaml.Node) []ValError {
	if n.Kind != yaml.SequenceNode {
		return []ValError{ve(file, n.Line, "ports must be array")}
	}
	var errs []ValError
	for _, p := range n.Content {
		if p.Kind != yaml.MappingNode {
			errs = append(errs, ve(file, p.Line, "ports must be object"))
			continue
		}
		cp := mustGet(p, "containerPort")
		if cp == nil {
			errs = append(errs, ve(file, 0, "containerPort is required"))
		} else {
			errs = append(errs, validatePortInt(file, cp, "containerPort")...)
		}
		if proto := get(p, "protocol"); proto != nil {
			if proto.Kind != yaml.ScalarNode || proto.Tag != "!!str" {
				errs = append(errs, ve(file, proto.Line, "protocol must be string"))
			} else {
				if proto.Value != "TCP" && proto.Value != "UDP" {
					errs = append(errs, ve(file, proto.Line, fmt.Sprintf("protocol has unsupported value '%s'", proto.Value)))
				}
			}
		}
	}
	return errs
}

func validateProbe(file string, n *yaml.Node, field string) []ValError {
	if n.Kind != yaml.MappingNode {
		return []ValError{ve(file, n.Line, fmt.Sprintf("%s must be object", field))}
	}
	httpGet := mustGet(n, "httpGet")
	if httpGet == nil {
		return []ValError{ve(file, 0, fmt.Sprintf("%s.httpGet is required", field))}
	}
	return validateHTTPGet(file, httpGet, field+".httpGet")
}

func validateHTTPGet(file string, n *yaml.Node, field string) []ValError {
	if n.Kind != yaml.MappingNode {
		return []ValError{ve(file, n.Line, fmt.Sprintf("%s must be object", field))}
	}
	var errs []ValError

	path := mustGet(n, "path")
	if path == nil {
		errs = append(errs, ve(file, 0, "path is required"))
	} else if path.Kind != yaml.ScalarNode || path.Tag != "!!str" {
		errs = append(errs, ve(file, path.Line, "path must be string"))
	} else {
		if !strings.HasPrefix(path.Value, "/") {
			errs = append(errs, ve(file, path.Line, fmt.Sprintf("path has invalid format '%s'", path.Value)))
		}
	}

	port := mustGet(n, "port")
	if port == nil {
		errs = append(errs, ve(file, 0, "port is required"))
	} else {
		errs = append(errs, validatePortInt(file, port, "port")...)
	}

	return errs
}

func validateResources(file string, n *yaml.Node) []ValError {
	if n.Kind != yaml.MappingNode {
		return []ValError{ve(file, n.Line, "resources must be object")}
	}
	var errs []ValError

	if req := get(n, "requests"); req != nil {
		errs = append(errs, validateResourceMap(file, req, "resources.requests")...)
	}
	if lim := get(n, "limits"); lim != nil {
		errs = append(errs, validateResourceMap(file, lim, "resources.limits")...)
	}

	return errs
}

func validateResourceMap(file string, n *yaml.Node, field string) []ValError {
	if n.Kind != yaml.MappingNode {
		return []ValError{ve(file, n.Line, fmt.Sprintf("%s must be object", field))}
	}
	var errs []ValError

	if cpu := get(n, "cpu"); cpu != nil {
		if cpu.Kind != yaml.ScalarNode {
			errs = append(errs, ve(file, cpu.Line, "cpu must be int"))
		} else {
			_, err := strconv.ParseInt(strings.TrimSpace(cpu.Value), 10, 64)
			if err != nil {
				errs = append(errs, ve(file, cpu.Line, "cpu must be int"))
			}
		}
	}
	if mem := get(n, "memory"); mem != nil {
		if mem.Kind != yaml.ScalarNode || mem.Tag != "!!str" {
			errs = append(errs, ve(file, mem.Line, "memory must be string"))
		} else {
			if !validMemory(mem.Value) {
				errs = append(errs, ve(file, mem.Line, fmt.Sprintf("memory has invalid format '%s'", mem.Value)))
			}
		}
	}

	return errs
}

// --- helpers ---

func firstDocument(root *yaml.Node) *yaml.Node {
	// root.Kind usually DocumentNode, root.Content[0] => MappingNode
	if root == nil {
		return nil
	}
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		return root.Content[0]
	}
	// sometimes yaml.Unmarshal gives root with Content as docs
	if len(root.Content) > 0 {
		return root.Content[0]
	}
	return nil
}

func mustGet(m *yaml.Node, key string) *yaml.Node {
	return get(m, key)
}

func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(m.Content); i += 2 {
		k := m.Content[i]
		v := m.Content[i+1]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return v
		}
	}
	return nil
}

func validateScalarType(file string, n *yaml.Node, field string, want string) []ValError {
	switch want {
	case "string":
		if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
			return []ValError{ve(file, n.Line, fmt.Sprintf("%s must be %s", field, want))}
		}
	default:
		return []ValError{ve(file, n.Line, fmt.Sprintf("%s must be %s", field, want))}
	}
	return nil
}

func validateStringEquals(file string, n *yaml.Node, field string, allowed []string) []ValError {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return []ValError{ve(file, n.Line, fmt.Sprintf("%s must be string", field))}
	}
	for _, a := range allowed {
		if n.Value == a {
			return nil
		}
	}
	return []ValError{ve(file, n.Line, fmt.Sprintf("%s has unsupported value '%s'", field, n.Value))}
}

func validatePortInt(file string, n *yaml.Node, field string) []ValError {
	if n.Kind != yaml.ScalarNode {
		return []ValError{ve(file, n.Line, fmt.Sprintf("%s must be int", field))}
	}
	v, err := strconv.Atoi(strings.TrimSpace(n.Value))
	if err != nil {
		return []ValError{ve(file, n.Line, fmt.Sprintf("%s must be int", field))}
	}
	if v <= 0 || v >= 65536 {
		return []ValError{ve(file, n.Line, fmt.Sprintf("%s value out of range", field))}
	}
	return nil
}

func validImage(s string) bool {
	// must be in registry.bigbrother.io and have tag
	if !strings.HasPrefix(s, "registry.bigbrother.io/") {
		return false
	}
	// tag required
	lastSlash := strings.LastIndex(s, "/")
	colon := strings.LastIndex(s, ":")
	return colon > lastSlash && colon < len(s)-1
}

var memRe = regexp.MustCompile(`^[0-9]+(Ki|Mi|Gi)$`)

func validMemory(s string) bool {
	return memRe.MatchString(strings.TrimSpace(s))
}

func ve(file string, line int, msg string) ValError {
	return ValError{File: file, Line: line, Msg: msg}
}

// If you want to treat some conditions as required-without-line:
func required(field string) ValError {
	return ValError{Msg: field + " is required"}
}

func wrapReadErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("cannot read file content: %w", err)
}

func wrapYAMLErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("cannot unmarshal file content: %w", err)
}

func is(err error, target error) bool {
	return errors.Is(err, target)
}
