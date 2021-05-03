package packages

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/xeipuuv/gojsonschema"

	"github.com/cdnjs/tools/util"

	"github.com/pkg/errors"
)

// GetHumanPackageJSONFiles gets the paths of the human-readable JSON files from within cdnjs/packages.
//
// TODO: update this to remove legacy ListFilesGlob
func GetHumanPackageJSONFiles(ctx context.Context) []string {
	list, err := util.ListFilesGlob(ctx, path.Join(util.GetHumanPackagesPath(), "packages"), "*/*.json")
	util.Check(err)
	return list
}

// ReadHumanJSON reads this package's human-readable JSON from within cdnjs/packages.
// It will validate the human-readable schema, returning an
// InvalidSchemaError if the schema is invalid.
func ReadHumanJSON(ctx context.Context, name string) (*Package, error) {
	return ReadHumanJSONFile(ctx, path.Join(util.GetHumanPackagesPath(), strings.ToLower(string(name[0])), name+".json"))
}

// ReadHumanJSONFile parses a JSON file into a Package.
// It will validate the human-readable schema, returning an
// InvalidSchemaError if the schema is invalid.
func ReadHumanJSONFile(ctx context.Context, file string) (*Package, error) {
	bytes, err := util.ReadHumanPackageSafely(file)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read %s", file)
	}

	// validate against human readable JSON schema
	res, err := HumanReadableSchema.Validate(gojsonschema.NewBytesLoader(bytes))
	if err != nil {
		// invalid JSON
		return nil, errors.Wrapf(err, "failed to parse %s", file)
	}

	if !res.Valid() {
		// invalid schema, so return result and custom error
		return nil, InvalidSchemaError{res}
	}

	return ReadHumanJSONBytes(ctx, file, bytes)
}

// Unmarshals the human-readable JSON into a *Package,
// setting the legacy `author` field if needed.
func ReadHumanJSONBytes(ctx context.Context, file string, bytes []byte) (*Package, error) {
	// unmarshal JSON into package
	var p Package
	if err := json.Unmarshal(bytes, &p); err != nil {
		return nil, errors.Wrapf(err, "failed to parse %s", file)
	}

	// if `authors` exists, parse `author` field
	if p.Authors != nil {
		author := parseAuthor(p.Authors)
		p.Author = &author
	}

	p.ctx = ctx
	return &p, nil
}

// ReadNonHumanJSONFile parses a JSON file into a Package.
// It will validate the non-human-readable schema, returning an
// InvalidSchemaError if the schema is invalid.
//
// TODO: THIS IS LEGACY FOR PACKAGE.MIN.JS GENERATION.
// REMOVE WHEN PACKAGE.MIN.JS IS GONE.
func ReadNonHumanJSONFile(ctx context.Context, file string) (*Package, error) {
	bytes, err := util.ReadLibFileSafely(file)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read %s", file)
	}

	return ReadNonHumanJSONBytes(ctx, file, bytes)
}

// ReadNonHumanJSONBytes unmarshals bytes into a *Package,
// validating against the non-human-readable schema, returning an
// InvalidSchemaError if the schema is invalid.
func ReadNonHumanJSONBytes(ctx context.Context, name string, bytes []byte) (*Package, error) {
	// validate the non-human readable JSON schema
	res, err := NonHumanReadableSchema.Validate(gojsonschema.NewBytesLoader(bytes))
	if err != nil {
		// invalid JSON
		return nil, errors.Wrapf(err, "failed to parse %s", name)
	}

	if !res.Valid() {
		// invalid schema, so return result and custom error
		return nil, InvalidSchemaError{res}
	}

	var p Package
	if err := json.Unmarshal(bytes, &p); err != nil {
		return nil, errors.Wrapf(err, "failed to parse %s", name)
	}

	// schema is valid, but we still need to ensure there are either
	// both `author` and `authors` fields or neither
	authorsNil, authorNil := p.Authors == nil, p.Author == nil
	if authorsNil != authorNil {
		return nil, errors.Wrapf(err, "`author` and `authors` must be either both nil or both non-nil - %s", name)
	}

	if !authorsNil {
		// `authors` exists, so need to verify `author` is parsed correctly
		author := *p.Author
		parsedAuthor := parseAuthor(p.Authors)
		if author != parsedAuthor {
			return nil, fmt.Errorf("author parse: actual `%s` != expected `%s`", author, parsedAuthor)
		}
	}

	p.ctx = ctx
	return &p, nil
}

// If `authors` exists, we need to parse `author` field
// for legacy compatibility with API.
func parseAuthor(authors []Author) string {
	var authorStrings []string
	for _, author := range authors {
		authorString := *author.Name
		if author.Email != nil {
			authorString += fmt.Sprintf(" <%s>", *author.Email)
		}
		if author.URL != nil {
			authorString += fmt.Sprintf(" (%s)", *author.URL)
		}
		authorStrings = append(authorStrings, authorString)
	}
	return strings.Join(authorStrings, ",")
}
