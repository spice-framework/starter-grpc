package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestValidateReleaseToolAuthorization(t *testing.T) {
	t.Parallel()
	valid := fmt.Sprintf(
		`{"Require":[{"Path":%q,"Version":%q}],"Tool":[{"Path":%q}]}`,
		developmentModule,
		developmentVersion,
		developmentTool,
	)
	for _, test := range []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "exact authorization", content: valid},
		{name: "missing tool", content: `{"Require":[]}`, wantErr: "exactly one"},
		{
			name: "wrong version",
			content: fmt.Sprintf(
				`{"Require":[{"Path":%q,"Version":"v0.0.0-wrong"}],"Tool":[{"Path":%q}]}`,
				developmentModule,
				developmentTool,
			),
			wantErr: "require exactly " + developmentVersion,
		},
		{
			name:    "missing requirement",
			content: fmt.Sprintf(`{"Require":[],"Tool":[{"Path":%q}]}`, developmentTool),
			wantErr: "must require " + developmentModule,
		},
		{name: "malformed metadata", content: `{`, wantErr: "decode release tool authorization"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateReleaseToolAuthorization([]byte(test.content))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validateReleaseToolAuthorization() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateReleaseToolAuthorization() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestDeterministicReleaseArtifactsRejectsDrift(t *testing.T) {
	t.Parallel()
	first := t.TempDir()
	second := t.TempDir()
	writeParityTestFile(t, first, "artifact", []byte("first"))
	writeParityTestFile(t, second, "artifact", []byte("second"))
	_, err := deterministicReleaseArtifacts("fixture", []string{first, second})
	if err == nil || !strings.Contains(err.Error(), "different artifacts") {
		t.Fatalf("deterministicReleaseArtifacts() error = %v", err)
	}
}

func TestValidateReleaseArchiveParity(t *testing.T) {
	t.Parallel()
	const prefix = "starter-grpc_0.0.0-rehearsal/"
	central := filepath.Join(t.TempDir(), "central.tar.gz")
	retained := filepath.Join(t.TempDir(), "retained.tar.gz")
	writeParityTestArchive(t, central, prefix, "same", "")
	writeParityTestArchive(t, retained, prefix, "same", "")
	if err := validateReleaseArchiveParity(central, prefix, retained, prefix); err != nil {
		t.Fatalf("validateReleaseArchiveParity() error = %v", err)
	}
	t.Run("entry drift", func(t *testing.T) {
		t.Parallel()
		drifted := filepath.Join(t.TempDir(), "drifted.tar.gz")
		writeParityTestArchive(t, drifted, prefix, "different", "")
		err := validateReleaseArchiveParity(central, prefix, drifted, prefix)
		if err == nil || !strings.Contains(err.Error(), "differ outside") {
			t.Fatalf("validateReleaseArchiveParity() error = %v", err)
		}
	})

	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string)
		want    string
	}{
		{
			name: "wrong root",
			prepare: func(t *testing.T, filename string) {
				t.Helper()
				writeParityTestArchive(t, filename, "wrong/", "same", "")
			},
			want: "outside required root",
		},
		{
			name: "hidden decompressed tail",
			prepare: func(t *testing.T, filename string) {
				t.Helper()
				writeParityTestArchive(t, filename, prefix, "same", "hidden")
			},
			want: "hidden trailing bytes",
		},
		{
			name: "extra gzip member",
			prepare: func(t *testing.T, filename string) {
				t.Helper()
				writeParityTestArchive(t, filename, prefix, "same", "")
				appendParityGzipMember(t, filename, "hidden")
			},
			want: "hidden trailing bytes",
		},
		{
			name: "raw trailing byte",
			prepare: func(t *testing.T, filename string) {
				t.Helper()
				writeParityTestArchive(t, filename, prefix, "same", "")
				appendParityBytes(t, filename, []byte("x"))
			},
			want: "hidden trailing bytes",
		},
		{
			name: "corrupt gzip trailer",
			prepare: func(t *testing.T, filename string) {
				t.Helper()
				writeParityTestArchive(t, filename, prefix, "same", "")
				content, err := os.ReadFile(filename)
				if err != nil {
					t.Fatal(err)
				}
				content[len(content)-8] ^= 0xff
				if err := os.WriteFile(filename, content, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "checksum",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			filename := filepath.Join(t.TempDir(), "wrong.tar.gz")
			test.prepare(t, filename)
			_, err := readParityArchive(filename, prefix)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("readParityArchive() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestReadBoundedParityArchiveRejectsOversizedFile(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "oversized.tar.gz")
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maximumParityArchiveSize + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedParityArchive(filename); err == nil ||
		!strings.Contains(err.Error(), "bounded") {
		t.Fatalf("readBoundedParityArchive() error = %v", err)
	}
}

func TestValidateParityTarMetadataRejectsNoncanonicalFields(t *testing.T) {
	t.Parallel()
	epoch := time.Unix(1_700_000_000, 0).UTC()
	canonical := func() *tar.Header {
		return &tar.Header{
			Name: "starter-grpc_0.0.0-rehearsal/file", Mode: 0o644, Size: 1,
			Typeflag: tar.TypeReg, ModTime: epoch, AccessTime: epoch,
			ChangeTime: epoch, Format: tar.FormatPAX,
			PAXRecords: map[string]string{"atime": "1700000000", "ctime": "1700000000"},
		}
	}
	if err := validateParityTarMetadata(
		canonical(), "starter-grpc_0.0.0-rehearsal/", "file", epoch,
	); err != nil {
		t.Fatalf("validateParityTarMetadata(canonical) = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*tar.Header)
	}{
		{name: "owner", mutate: func(header *tar.Header) { header.Uid = 1 }},
		{name: "device", mutate: func(header *tar.Header) { header.Devmajor = 1 }},
		{name: "regular link", mutate: func(header *tar.Header) { header.Linkname = "target" }},
		{name: "unsupported type", mutate: func(header *tar.Header) { header.Typeflag = tar.TypeDir }},
		{name: "extra PAX", mutate: func(header *tar.Header) { header.PAXRecords["comment"] = "hidden" }},
		{name: "wrong timestamp", mutate: func(header *tar.Header) { header.AccessTime = epoch.Add(time.Second) }},
		{name: "extended attribute", mutate: func(header *tar.Header) {
			//nolint:staticcheck // Exercise rejection of archive/tar's legacy Xattrs compatibility view.
			header.Xattrs = map[string]string{"user.spice": "hidden"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			header := canonical()
			test.mutate(header)
			if err := validateParityTarMetadata(
				header, "starter-grpc_0.0.0-rehearsal/", "file", epoch,
			); err == nil {
				t.Fatal("validateParityTarMetadata() error = nil")
			}
		})
	}
}

func TestValidateReleaseSBOMParity(t *testing.T) {
	t.Parallel()
	central, retained := paritySBOMFixtures()
	for _, test := range []struct {
		name    string
		mutate  func(*releaseSBOM, *releaseSBOM)
		wantErr string
	}{
		{name: "documented provenance differences"},
		{
			name: "package drift",
			mutate: func(_ *releaseSBOM, retained *releaseSBOM) {
				retained.Packages[0].VersionInfo = "v9.9.9"
			},
			wantErr: "outside documented provenance",
		},
		{
			name: "wrong retained provenance",
			mutate: func(_ *releaseSBOM, retained *releaseSBOM) {
				retained.CreationInfo.Creators[0] = "Organization: Unknown"
			},
			wantErr: "gRPC builder",
		},
		{
			name: "relationship drift",
			mutate: func(_ *releaseSBOM, retained *releaseSBOM) {
				retained.Relationships = retained.Relationships[:1]
			},
			wantErr: "outside documented provenance",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			centralCopy := cloneParitySBOM(t, central)
			retainedCopy := cloneParitySBOM(t, retained)
			if test.mutate != nil {
				test.mutate(&centralCopy, &retainedCopy)
			}
			err := validateReleaseSBOMParity(
				marshalParitySBOM(t, centralCopy),
				marshalParitySBOM(t, retainedCopy),
			)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validateReleaseSBOMParity() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateReleaseSBOMParity() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateReleaseParityRejectsSignaturesAndBadChecksums(t *testing.T) {
	t.Parallel()
	centralRoot, central := writeParityTestRelease(t, true)
	retainedRoot, retained := writeParityTestRelease(t, false)
	if err := validateReleaseParity(centralRoot, central, retainedRoot, retained); err != nil {
		t.Fatalf("validateReleaseParity() error = %v", err)
	}

	t.Run("signature", func(t *testing.T) {
		t.Parallel()
		signed := maps.Clone(central)
		signed["checksums.txt.sig"] = sha256.Sum256([]byte("signature"))
		err := validateReleaseParity(centralRoot, signed, retainedRoot, retained)
		if err == nil || !strings.Contains(err.Error(), "signatures are forbidden") {
			t.Fatalf("validateReleaseParity() error = %v", err)
		}
	})

	t.Run("checksum", func(t *testing.T) {
		t.Parallel()
		corruptRoot, corrupt := writeParityTestRelease(t, true)
		writeParityTestFile(t, corruptRoot, "checksums.txt", []byte("invalid\n"))
		corrupt["checksums.txt"] = sha256.Sum256([]byte("invalid\n"))
		err := validateReleaseParity(corruptRoot, corrupt, retainedRoot, retained)
		if err == nil || !strings.Contains(err.Error(), "checksums.txt") {
			t.Fatalf("validateReleaseParity() error = %v", err)
		}
	})

	t.Run("noncanonical line endings", func(t *testing.T) {
		t.Parallel()
		corruptRoot, corrupt := writeParityTestRelease(t, true)
		content, err := os.ReadFile(filepath.Join(corruptRoot, "checksums.txt"))
		if err != nil {
			t.Fatal(err)
		}
		content = []byte(strings.ReplaceAll(string(content), "\n", "\r\n"))
		writeParityTestFile(t, corruptRoot, "checksums.txt", content)
		corrupt["checksums.txt"] = sha256.Sum256(content)
		err = validateReleaseParity(corruptRoot, corrupt, retainedRoot, retained)
		if err == nil || !strings.Contains(err.Error(), "canonical LF") {
			t.Fatalf("validateReleaseParity() error = %v", err)
		}
	})
}

func paritySBOMFixtures() (releaseSBOM, releaseSBOM) {
	root := sbomPackage{
		Name: modulePath, SPDXID: "SPDXRef-Package-root", VersionInfo: rehearsalVersion,
		DownloadLocation: "NOASSERTION", LicenseConcluded: "NOASSERTION",
		LicenseDeclared: "NOASSERTION", CopyrightText: "NOASSERTION",
	}
	dependency := sbomPackage{
		Name: "example.com/dependency", SPDXID: "SPDXRef-Package-dependency", VersionInfo: "v1.2.3",
		DownloadLocation: "NOASSERTION", LicenseConcluded: "NOASSERTION",
		LicenseDeclared: "NOASSERTION", CopyrightText: "NOASSERTION",
	}
	describes := sbomRelationship{
		SPDXElementID: "SPDXRef-DOCUMENT", RelationshipType: "DESCRIBES",
		RelatedSPDXElement: root.SPDXID,
	}
	dependsOn := sbomRelationship{
		SPDXElementID: root.SPDXID, RelationshipType: "DEPENDS_ON",
		RelatedSPDXElement: dependency.SPDXID,
	}
	common := releaseSBOM{
		SPDXVersion: "SPDX-2.3", DataLicense: "CC0-1.0", SPDXID: "SPDXRef-DOCUMENT",
		CreationInfo:  sbomCreationInfo{Created: "2026-01-01T00:00:00Z"},
		Packages:      []sbomPackage{root, dependency},
		Relationships: []sbomRelationship{describes, dependsOn},
	}
	central := common
	central.Name = "starter-grpc " + rehearsalVersion
	central.DocumentNamespace = parityNamespace("v1/", 'a')
	central.CreationInfo.Creators = []string{
		"Organization: Spice Framework",
		"Tool: github.com/spice-framework/development/cmd/spice-dev library-release renderer/v1",
	}
	retained := cloneParitySBOMValue(common)
	retained.Name = "Spice gRPC starter " + rehearsalVersion
	retained.DocumentNamespace = parityNamespace("", 'b')
	retained.CreationInfo.Creators = []string{
		"Organization: Spice Authors",
		"Tool: github.com/spice-framework/starter-grpc/cmd/starter-grpc-release",
	}
	return central, retained
}

func writeParityTestRelease(t *testing.T, central bool) (string, map[string][sha256.Size]byte) {
	t.Helper()
	root := t.TempDir()
	centralSBOM, retainedSBOM := paritySBOMFixtures()
	sbom := retainedSBOM
	if central {
		sbom = centralSBOM
	}
	archiveName := releaseArchiveName()
	sbomName := releaseSBOMName()
	prefix := "starter-grpc_0.0.0-rehearsal/"
	writeParityTestArchive(t, filepath.Join(root, archiveName), prefix, "same", "")
	writeParityTestFile(t, root, sbomName, marshalParitySBOM(t, sbom))
	archive, err := os.ReadFile(filepath.Join(root, archiveName))
	if err != nil {
		t.Fatal(err)
	}
	sbomContent, err := os.ReadFile(filepath.Join(root, sbomName))
	if err != nil {
		t.Fatal(err)
	}
	checksums := fmt.Sprintf(
		"%x  %s\n%x  %s\n",
		sha256.Sum256(sbomContent),
		sbomName,
		sha256.Sum256(archive),
		archiveName,
	)
	writeParityTestFile(t, root, "checksums.txt", []byte(checksums))
	digests, err := treeDigests(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, digests
}

func writeParityTestArchive(
	t *testing.T,
	filename string,
	prefix string,
	content string,
	hiddenTail string,
) {
	t.Helper()
	file, err := os.Create(filename) // #nosec G304 -- test path is inside t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	epoch := time.Unix(1_700_000_000, 0).UTC()
	gzipWriter.ModTime = epoch
	gzipWriter.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	entryName := strings.Repeat("nested/", 16) + "README.md"
	header := tar.Header{
		Name: prefix + entryName, Mode: 0o644, Size: int64(len(content)),
		Typeflag: tar.TypeReg, ModTime: epoch, AccessTime: epoch,
		ChangeTime: epoch, Format: tar.FormatPAX,
	}
	if err := tarWriter.WriteHeader(&header); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := gzipWriter.Write([]byte(hiddenTail)); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func appendParityGzipMember(t *testing.T, filename string, content string) {
	t.Helper()
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0) // #nosec G304 -- test path is inside t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.ModTime = time.Unix(1_700_000_000, 0).UTC()
	gzipWriter.OS = 255
	if _, err := gzipWriter.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func appendParityBytes(t *testing.T, filename string, content []byte) {
	t.Helper()
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0) // #nosec G304 -- test path is inside t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func releaseArchiveName() string {
	return "starter-grpc_" + strings.TrimPrefix(rehearsalVersion, "v") + "_source.tar.gz"
}

func releaseSBOMName() string {
	return "starter-grpc_" + strings.TrimPrefix(rehearsalVersion, "v") + "_sbom.spdx.json"
}

func parityNamespace(extra string, digit rune) string {
	return "https://github.com/spice-framework/starter-grpc/releases/" + rehearsalVersion +
		"/spdx/" + extra + strings.Repeat(string(digit), sha256.Size*2)
}

func cloneParitySBOM(t *testing.T, value releaseSBOM) releaseSBOM {
	t.Helper()
	content := marshalParitySBOM(t, value)
	cloned, err := decodeReleaseSBOM(content)
	if err != nil {
		t.Fatal(err)
	}
	return cloned
}

func cloneParitySBOMValue(value releaseSBOM) releaseSBOM {
	content, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var result releaseSBOM
	if err := json.Unmarshal(content, &result); err != nil {
		panic(err)
	}
	return result
}

func marshalParitySBOM(t *testing.T, value releaseSBOM) []byte {
	t.Helper()
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func writeParityTestFile(t *testing.T, root string, name string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseArtifactNamesRemainSorted(t *testing.T) {
	t.Parallel()
	names := []string{"checksums.txt", releaseSBOMName(), releaseArchiveName()}
	if !slices.IsSorted(names) {
		t.Fatalf("release artifact names are not canonical: %v", names)
	}
}
