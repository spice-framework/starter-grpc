package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	developmentModule        = "github.com/spice-framework/development"
	developmentTool          = developmentModule + "/cmd/spice-dev"
	developmentVersion       = "v0.0.0-20260806034648-1856466df09d"
	rehearsalVersion         = "v0.0.0-rehearsal"
	maximumParityArchiveSize = 256 << 20
	maximumParityEntrySize   = 128 << 20
	maximumParityEntries     = 100_000
)

func requireReleaseTool(ctx context.Context, root string) error {
	content, err := capture(ctx, root, nil, "go", "mod", "edit", "-json")
	if err != nil {
		return fmt.Errorf("read release tool authorization: %w", err)
	}
	return validateReleaseToolAuthorization([]byte(content))
}

func validateReleaseToolAuthorization(content []byte) error {
	var metadata struct {
		Require []struct {
			Path    string
			Version string
		}
		Tool []struct {
			Path string
		}
	}
	if err := json.Unmarshal(content, &metadata); err != nil {
		return fmt.Errorf("decode release tool authorization: %w", err)
	}
	toolCount := 0
	for _, tool := range metadata.Tool {
		if tool.Path == developmentTool {
			toolCount++
		}
	}
	if toolCount != 1 {
		return fmt.Errorf(
			"go.mod must authorize exactly one %s tool declaration; found %d",
			developmentTool,
			toolCount,
		)
	}
	for _, requirement := range metadata.Require {
		if requirement.Path != developmentModule {
			continue
		}
		if requirement.Version != developmentVersion {
			return fmt.Errorf(
				"go.mod selects release tool %s; require exactly %s",
				requirement.Version,
				developmentVersion,
			)
		}
		return nil
	}
	return fmt.Errorf("go.mod must require %s at exactly %s", developmentModule, developmentVersion)
}

func releaseParity(ctx context.Context, root string) error {
	parent, err := os.MkdirTemp("", "starter-grpc-release-parity-*")
	if err != nil {
		return fmt.Errorf("create release parity root: %w", err)
	}
	defer removeTree(parent)

	offlineVendor := map[string]string{"GOFLAGS": "-mod=vendor"}
	resolved, err := capture(ctx, root, offlineVendor, "go", "tool", "-n", developmentTool)
	if err != nil {
		return fmt.Errorf("resolve authorized central release tool: %w", err)
	}
	if strings.TrimSpace(resolved) == "" {
		return errors.New("resolve authorized central release tool: empty executable path")
	}
	plan, err := capture(
		ctx,
		root,
		offlineVendor,
		"go",
		"tool",
		developmentTool,
		"library-release",
		"plan",
		"--root="+root,
		"--repo=starter-grpc",
		"--version="+rehearsalVersion,
		"--rehearsal",
	)
	if err != nil {
		return fmt.Errorf("plan central release rehearsal: %w", err)
	}
	planFile := filepath.Join(parent, "plan.json")
	if writeErr := os.WriteFile(planFile, []byte(plan+"\n"), 0o600); writeErr != nil {
		return fmt.Errorf("write central release rehearsal plan: %w", writeErr)
	}
	centralOutputs := []string{
		filepath.Join(parent, "central-first"),
		filepath.Join(parent, "central-second"),
	}
	for _, outputDir := range centralOutputs {
		if commandErr := command(
			ctx,
			root,
			offlineVendor,
			"go",
			"tool",
			developmentTool,
			"library-release",
			"render",
			"--root="+root,
			"--plan="+planFile,
			"--output="+outputDir,
		); commandErr != nil {
			return fmt.Errorf("render central release rehearsal: %w", commandErr)
		}
	}
	retainedOutputs := []string{
		filepath.Join(parent, "retained-first"),
		filepath.Join(parent, "retained-second"),
	}
	for _, outputDir := range retainedOutputs {
		if commandErr := command(
			ctx,
			root,
			offlineVendor,
			"go",
			"run",
			"./cmd/starter-grpc-release",
			"-rehearsal",
			"-version="+rehearsalVersion,
			"-output="+outputDir,
		); commandErr != nil {
			return fmt.Errorf("render retained release rehearsal: %w", commandErr)
		}
	}
	central, err := deterministicReleaseArtifacts("central", centralOutputs)
	if err != nil {
		return err
	}
	retained, err := deterministicReleaseArtifacts("retained", retainedOutputs)
	if err != nil {
		return err
	}
	return validateReleaseParity(centralOutputs[0], central, retainedOutputs[0], retained)
}

func deterministicReleaseArtifacts(
	name string,
	outputs []string,
) (map[string][sha256.Size]byte, error) {
	first, err := treeDigests(outputs[0])
	if err != nil {
		return nil, err
	}
	second, err := treeDigests(outputs[1])
	if err != nil {
		return nil, err
	}
	if !maps.Equal(first, second) {
		return nil, fmt.Errorf("identical %s release rehearsals produced different artifacts", name)
	}
	return first, nil
}

func validateReleaseParity(
	centralRoot string,
	central map[string][sha256.Size]byte,
	retainedRoot string,
	retained map[string][sha256.Size]byte,
) error {
	base := "starter-grpc_" + strings.TrimPrefix(rehearsalVersion, "v")
	archiveName := base + "_source.tar.gz"
	sbomName := base + "_sbom.spdx.json"
	expected := []string{"checksums.txt", sbomName, archiveName}
	for name, artifacts := range map[string]map[string][sha256.Size]byte{
		"central": central, "retained": retained,
	} {
		actual := slices.Sorted(maps.Keys(artifacts))
		if !slices.Equal(actual, expected) {
			return fmt.Errorf(
				"%s release rehearsal artifacts %v do not match %v; signatures are forbidden",
				name,
				actual,
				expected,
			)
		}
	}
	if err := validateReleaseChecksums(centralRoot, central, sbomName, archiveName); err != nil {
		return fmt.Errorf("central release rehearsal: %w", err)
	}
	if err := validateReleaseChecksums(retainedRoot, retained, sbomName, archiveName); err != nil {
		return fmt.Errorf("retained release rehearsal: %w", err)
	}
	prefix := base + "/"
	if err := validateReleaseArchiveParity(
		filepath.Join(centralRoot, archiveName),
		prefix,
		filepath.Join(retainedRoot, archiveName),
		prefix,
	); err != nil {
		return err
	}
	if central[archiveName] != retained[archiveName] {
		return errors.New("central and retained source archives are not byte-identical")
	}
	centralSBOM, err := readReleaseArtifact(centralRoot, sbomName)
	if err != nil {
		return err
	}
	retainedSBOM, err := readReleaseArtifact(retainedRoot, sbomName)
	if err != nil {
		return err
	}
	return validateReleaseSBOMParity(centralSBOM, retainedSBOM)
}

func validateReleaseChecksums(
	root string,
	artifacts map[string][sha256.Size]byte,
	names ...string,
) error {
	content, err := readReleaseArtifact(root, "checksums.txt")
	if err != nil {
		return err
	}
	if len(content) == 0 || content[len(content)-1] != '\n' || bytes.ContainsRune(content, '\r') {
		return errors.New("checksums.txt must use canonical LF-terminated lines")
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	orderedNames := slices.Clone(names)
	slices.Sort(orderedNames)
	if len(lines) != len(orderedNames) {
		return fmt.Errorf("checksums.txt has %d lines; require %d", len(lines), len(orderedNames))
	}
	for index, name := range orderedNames {
		want := fmt.Sprintf("%x  %s", artifacts[name], name)
		if lines[index] != want {
			return fmt.Errorf("checksums.txt line %d is %q; require canonical %q", index+1, lines[index], want)
		}
	}
	return nil
}

type parityArchive struct {
	Gzip    parityGzipHeader
	Entries []parityArchiveEntry
}

type parityGzipHeader struct {
	ModTime time.Time
	OS      byte
}

type parityArchiveEntry struct {
	Name       string
	Linkname   string
	Size       int64
	Mode       int64
	ModTime    time.Time
	AccessTime time.Time
	ChangeTime time.Time
	Typeflag   byte
	PAXRecords map[string]string
	Digest     [sha256.Size]byte
}

func validateReleaseArchiveParity(
	centralPath string,
	centralPrefix string,
	retainedPath string,
	retainedPrefix string,
) error {
	central, err := readParityArchive(centralPath, centralPrefix)
	if err != nil {
		return fmt.Errorf("read central source archive: %w", err)
	}
	retained, err := readParityArchive(retainedPath, retainedPrefix)
	if err != nil {
		return fmt.Errorf("read retained source archive: %w", err)
	}
	if !reflect.DeepEqual(central, retained) {
		return errors.New("central and retained source archives differ outside their declared roots")
	}
	return nil
}

func readParityArchive(filename string, expectedPrefix string) (parityArchive, error) {
	content, err := readBoundedParityArchive(filename)
	if err != nil {
		return parityArchive{}, err
	}
	compressed := bytes.NewReader(content)
	gzipReader, err := gzip.NewReader(compressed)
	if err != nil {
		return parityArchive{}, err
	}
	gzipReader.Multistream(false)
	if gzipReader.Comment != "" || len(gzipReader.Extra) != 0 || gzipReader.Name != "" ||
		gzipReader.OS != 255 || gzipReader.ModTime.IsZero() {
		return parityArchive{}, errors.Join(
			errors.New("source gzip has noncanonical metadata"),
			gzipReader.Close(),
		)
	}
	result := parityArchive{Gzip: parityGzipHeader{ModTime: gzipReader.ModTime, OS: gzipReader.OS}}
	entries, err := readParityArchiveEntries(tar.NewReader(gzipReader), expectedPrefix, gzipReader.ModTime)
	if err != nil {
		return parityArchive{}, errors.Join(err, gzipReader.Close())
	}
	result.Entries = entries
	remaining, err := io.Copy(io.Discard, io.LimitReader(gzipReader, maximumParityArchiveSize+1))
	if err != nil {
		return parityArchive{}, errors.Join(err, gzipReader.Close())
	}
	if remaining != 0 {
		return parityArchive{}, errors.Join(
			fmt.Errorf("decompressed archive has %d hidden trailing bytes", remaining),
			gzipReader.Close(),
		)
	}
	if err := gzipReader.Close(); err != nil {
		return parityArchive{}, err
	}
	if compressed.Len() != 0 {
		return parityArchive{}, fmt.Errorf("compressed archive has %d hidden trailing bytes", compressed.Len())
	}
	return result, nil
}

func readBoundedParityArchive(filename string) (_ []byte, resultErr error) {
	file, err := os.Open(filename) // #nosec G304 -- caller supplies a generated temporary artifact path.
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximumParityArchiveSize {
		return nil, fmt.Errorf("compressed archive is not a regular file bounded to %d bytes", maximumParityArchiveSize)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumParityArchiveSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maximumParityArchiveSize {
		return nil, fmt.Errorf("compressed archive exceeds %d bytes", maximumParityArchiveSize)
	}
	return content, nil
}

func readParityArchiveEntries(
	tarReader *tar.Reader,
	expectedPrefix string,
	epoch time.Time,
) ([]parityArchiveEntry, error) {
	seen := make(map[string]struct{})
	entries := make([]parityArchiveEntry, 0)
	var total int64
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(entries) >= maximumParityEntries {
			return nil, fmt.Errorf("source archive exceeds %d entries", maximumParityEntries)
		}
		entry, err := readParityArchiveEntry(tarReader, header, expectedPrefix, epoch, seen, &total)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func readParityArchiveEntry(
	tarReader *tar.Reader,
	header *tar.Header,
	expectedPrefix string,
	epoch time.Time,
	seen map[string]struct{},
	total *int64,
) (parityArchiveEntry, error) {
	name, found := strings.CutPrefix(header.Name, expectedPrefix)
	if !found || !safeParityPath(name) {
		return parityArchiveEntry{}, fmt.Errorf(
			"archive entry %q is outside required root %q",
			header.Name,
			expectedPrefix,
		)
	}
	if _, duplicate := seen[name]; duplicate {
		return parityArchiveEntry{}, fmt.Errorf("archive entry %q is duplicated", name)
	}
	seen[name] = struct{}{}
	if header.Size < 0 || header.Size > maximumParityEntrySize ||
		*total > maximumParityArchiveSize-header.Size {
		return parityArchiveEntry{}, fmt.Errorf("archive entry %q exceeds parity bounds", name)
	}
	if err := validateParityTarMetadata(header, expectedPrefix, name, epoch); err != nil {
		return parityArchiveEntry{}, err
	}
	*total += header.Size
	digest := sha256.New()
	if _, err := io.CopyN(digest, tarReader, header.Size); err != nil {
		return parityArchiveEntry{}, err
	}
	var contentDigest [sha256.Size]byte
	copy(contentDigest[:], digest.Sum(nil))
	paxRecords := maps.Clone(header.PAXRecords)
	if _, present := paxRecords["path"]; present {
		paxRecords["path"] = name
	}
	return parityArchiveEntry{
		Name: name, Linkname: header.Linkname, Size: header.Size, Mode: header.Mode,
		ModTime: header.ModTime, AccessTime: header.AccessTime, ChangeTime: header.ChangeTime,
		Typeflag: header.Typeflag, PAXRecords: paxRecords, Digest: contentDigest,
	}, nil
}

func validateParityTarMetadata(
	header *tar.Header,
	expectedPrefix string,
	name string,
	epoch time.Time,
) error {
	if header.Format != tar.FormatPAX || !header.ModTime.Equal(epoch) ||
		!header.AccessTime.Equal(epoch) || !header.ChangeTime.Equal(epoch) ||
		header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" ||
		header.Devmajor != 0 || header.Devminor != 0 {
		return fmt.Errorf("archive entry %q has noncanonical metadata", header.Name)
	}
	//nolint:staticcheck // The parity boundary explicitly rejects archive/tar's legacy Xattrs view.
	if len(header.Xattrs) != 0 {
		return fmt.Errorf("archive entry %q has extended attributes", header.Name)
	}
	if typeErr := validateParityTarType(header, name); typeErr != nil {
		return typeErr
	}
	wantPAX := expectedParityPAXRecords(header, expectedPrefix, name, epoch)
	if !maps.Equal(header.PAXRecords, wantPAX) {
		return fmt.Errorf("archive entry %q has noncanonical PAX metadata", header.Name)
	}
	return nil
}

func expectedParityPAXRecords(
	header *tar.Header,
	expectedPrefix string,
	name string,
	epoch time.Time,
) map[string]string {
	wantPAX := map[string]string{
		"atime": strconv.FormatInt(epoch.Unix(), 10),
		"ctime": strconv.FormatInt(epoch.Unix(), 10),
	}
	if !asciiParityValue(header.Name) || len(header.Name) > 100 {
		wantPAX["path"] = expectedPrefix + name
	}
	if header.Typeflag == tar.TypeSymlink &&
		(!asciiParityValue(header.Linkname) || len(header.Linkname) > 100) {
		wantPAX["linkpath"] = header.Linkname
	}
	return wantPAX
}

func validateParityTarType(header *tar.Header, name string) error {
	switch header.Typeflag {
	case tar.TypeReg:
		if header.Mode != 0o644 && header.Mode != 0o755 || header.Linkname != "" {
			return fmt.Errorf("archive regular file %q has noncanonical mode or link metadata", header.Name)
		}
	case tar.TypeSymlink:
		if header.Mode != 0o777 || header.Size != 0 || !safeParityLink(name, header.Linkname) {
			return fmt.Errorf("archive symlink %q has noncanonical metadata", header.Name)
		}
	default:
		return fmt.Errorf("archive entry %q has unsupported type", header.Name)
	}
	return nil
}

func safeParityPath(name string) bool {
	return name != "" && utf8.ValidString(name) && !strings.ContainsRune(name, 0) &&
		!strings.Contains(name, "\\") && path.Clean(name) == name && name != "." &&
		name != ".." && !strings.HasPrefix(name, "../") && !path.IsAbs(name)
}

func safeParityLink(name string, target string) bool {
	if target == "" || !utf8.ValidString(target) || strings.ContainsRune(target, 0) ||
		strings.Contains(target, "\\") || path.Clean(target) != target || path.IsAbs(target) {
		return false
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	return resolved != ".." && !strings.HasPrefix(resolved, "../") && !path.IsAbs(resolved)
}

func asciiParityValue(value string) bool {
	for index := range len(value) {
		if value[index] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

type releaseSBOM struct {
	SPDXVersion       string             `json:"spdxVersion"`
	DataLicense       string             `json:"dataLicense"`
	SPDXID            string             `json:"SPDXID"`
	Name              string             `json:"name"`
	DocumentNamespace string             `json:"documentNamespace"`
	CreationInfo      sbomCreationInfo   `json:"creationInfo"`
	Packages          []sbomPackage      `json:"packages"`
	Relationships     []sbomRelationship `json:"relationships"`
}

type sbomCreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type sbomPackage struct {
	Name             string            `json:"name"`
	SPDXID           string            `json:"SPDXID"`
	VersionInfo      string            `json:"versionInfo"`
	DownloadLocation string            `json:"downloadLocation"`
	FilesAnalyzed    bool              `json:"filesAnalyzed"`
	LicenseConcluded string            `json:"licenseConcluded"`
	LicenseDeclared  string            `json:"licenseDeclared"`
	CopyrightText    string            `json:"copyrightText"`
	ExternalRefs     []sbomExternalRef `json:"externalRefs,omitempty"`
}

type sbomExternalRef struct {
	ReferenceCategory string `json:"referenceCategory"`
	ReferenceType     string `json:"referenceType"`
	ReferenceLocator  string `json:"referenceLocator"`
}

type sbomRelationship struct {
	SPDXElementID      string `json:"spdxElementId"`
	RelationshipType   string `json:"relationshipType"`
	RelatedSPDXElement string `json:"relatedSpdxElement"`
}

func validateReleaseSBOMParity(centralContent []byte, retainedContent []byte) error {
	central, err := decodeReleaseSBOM(centralContent)
	if err != nil {
		return fmt.Errorf("decode central release SBOM: %w", err)
	}
	retained, err := decodeReleaseSBOM(retainedContent)
	if err != nil {
		return fmt.Errorf("decode retained release SBOM: %w", err)
	}
	baseNamespace := "https://github.com/spice-framework/starter-grpc/releases/" +
		rehearsalVersion + "/spdx/"
	if central.Name != "starter-grpc "+rehearsalVersion ||
		!validSBOMNamespace(central.DocumentNamespace, baseNamespace+"v1/") ||
		!slices.Equal(central.CreationInfo.Creators, []string{
			"Organization: Spice Framework",
			"Tool: github.com/spice-framework/development/cmd/spice-dev library-release renderer/v1",
		}) {
		return errors.New("central release SBOM provenance does not match renderer/v1")
	}
	if retained.Name != "Spice gRPC starter "+rehearsalVersion ||
		!validSBOMNamespace(retained.DocumentNamespace, baseNamespace) ||
		strings.HasPrefix(retained.DocumentNamespace, baseNamespace+"v1/") ||
		!slices.Equal(retained.CreationInfo.Creators, []string{
			"Organization: Spice Authors",
			"Tool: github.com/spice-framework/starter-grpc/cmd/starter-grpc-release",
		}) {
		return errors.New("retained release SBOM provenance does not match the gRPC builder")
	}
	if central.DocumentNamespace == retained.DocumentNamespace {
		return errors.New("central and retained SBOM namespaces must identify their distinct builders")
	}
	central.Name = retained.Name
	central.DocumentNamespace = retained.DocumentNamespace
	central.CreationInfo.Creators = slices.Clone(retained.CreationInfo.Creators)
	if !reflect.DeepEqual(central, retained) {
		return errors.New("central and retained SBOMs differ outside documented provenance fields")
	}
	return nil
}

func decodeReleaseSBOM(content []byte) (releaseSBOM, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var result releaseSBOM
	if err := decoder.Decode(&result); err != nil {
		return releaseSBOM{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return releaseSBOM{}, errors.New("release SBOM has trailing JSON values")
		}
		return releaseSBOM{}, err
	}
	return result, nil
}

func validSBOMNamespace(value string, prefix string) bool {
	digest, found := strings.CutPrefix(value, prefix)
	if !found || len(digest) != sha256.Size*2 {
		return false
	}
	for _, character := range digest {
		decimal := character >= '0' && character <= '9'
		hexadecimal := character >= 'a' && character <= 'f'
		if !decimal && !hexadecimal {
			return false
		}
	}
	return true
}

func readReleaseArtifact(rootPath string, name string) (_ []byte, resultErr error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open release artifact root: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	content, err := root.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read release artifact %q: %w", name, err)
	}
	return content, nil
}
