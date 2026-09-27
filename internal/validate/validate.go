// Package validate reproduces Chroma's server-side validation rules and
// error messages.
package validate

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/wire"
)

var alnumRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,510}[a-zA-Z0-9]$`)

// Name validates a collection or database name (validators::validate_name).
func Name(name string) error {
	if topo, rest, ok := strings.Cut(name, "+"); ok {
		if len(name) > 512 {
			return apierr.Validation("name", "Expected a name containing 3-512 characters. Got: %d", len(name))
		}
		if strings.Contains(rest, "+") {
			return apierr.Validation("name", "Expected a name to contain at most one topology:  Got two `+` characters.")
		}
		if err := Name(topo); err != nil {
			return err
		}
		return Name(rest)
	}
	if !alnumRE.MatchString(name) {
		return apierr.Validation("name", "Expected a name containing 3-512 characters from [a-zA-Z0-9._-], starting and ending with a character in [a-zA-Z0-9]. Got: %s", name)
	}
	if strings.Contains(name, "..") {
		return apierr.Validation("name", "Expected a name that does not contains two consecutive periods (..). Got %s", name)
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return apierr.Validation("name", "Expected a name that is not a valid ip address. Got %s", name)
	}
	return nil
}

// DatabaseName validates a new database name. Chroma reports these errors
// without the "Validation error: name:" prefix.
func DatabaseName(name string) error {
	if err := Name(name); err != nil {
		ae := err.(*apierr.Error)
		return apierr.InvalidArgument("%s", strings.TrimPrefix(ae.Message, "Validation error: name: "))
	}
	return nil
}

// TenantName validates a tenant name (min length 3).
func TenantName(name string) error {
	if len([]rune(name)) < 3 {
		return apierr.Validation("name", `Validation error: length [{"min": Number(3), "value": String(%q)}]`, name)
	}
	return nil
}

// DatabasePathName validates the {database} path parameter.
func DatabasePathName(name string) error {
	if len(name) < 3 {
		return apierr.InvalidArgument("database name must be at least 3 characters")
	}
	return nil
}

// MetadataKey validates one metadata key.
func MetadataKey(key string) error {
	if key == "" {
		return fmt.Errorf("Metadata key cannot be empty")
	}
	if strings.HasPrefix(key, "#") || strings.HasPrefix(key, "$") {
		return fmt.Errorf("Metadata key cannot start with '#' or '$': %s", key)
	}
	return nil
}

func metadataValues(md map[string]*wire.Value) error {
	for k, v := range md {
		if err := MetadataKey(k); err != nil {
			return err
		}
		if v != nil && v.Kind == wire.KindSparse {
			if err := v.Sparse.Validate(); err != nil {
				return fmt.Errorf("Invalid sparse vector: %s", err.Error())
			}
		}
	}
	return nil
}

// RecordMetadatas validates per-record metadata maps.
func RecordMetadatas(mds []wire.UpdateMetadata) error {
	for i, md := range mds {
		if md == nil {
			continue
		}
		if err := metadataValues(md); err != nil {
			return apierr.Validation("metadatas", "Invalid metadata at index %d", i)
		}
	}
	return nil
}

// CollectionMetadata validates collection metadata (must be non-empty).
// field is the payload field name used in error messages.
func CollectionMetadata(md wire.Metadata, field string) error {
	if md == nil {
		return nil
	}
	if len(md) == 0 {
		return apierr.Validation(field, "Metadata cannot be empty")
	}
	conv := make(map[string]*wire.Value, len(md))
	for k, v := range md {
		v := v
		conv[k] = &v
	}
	if err := metadataValues(conv); err != nil {
		return apierr.Validation(field, "%s", err.Error())
	}
	return nil
}
