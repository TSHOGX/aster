package examples

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

type PatchArtifact struct {
	Reference evolution.ArtifactRef `json:"reference"`
	Bytes     []byte                `json:"-"`
}
type PatchCandidate struct {
	Patch     evolution.Patch `json:"patch"`
	Artifacts []PatchArtifact `json:"artifacts"`
	NoChange  bool            `json:"no_change"`
}

// CandidatePatch is pure admission: every path/profile/base/budget check precedes
// any artifact writes. The caller must authorize and seal returned artifacts and
// obtain current reservation/run effect authority before submitting to Studio.
// These example builders only add/replace their declared files; they never infer
// deletion from an absent proposal or overwrite another template's root indexes.
func CandidatePatch(run evolution.Run, fence int64, candidate Candidate, base []manifest.Entry, grant string) (PatchCandidate, error) {
	result := PatchCandidate{Patch: evolution.Patch{RunID: run.ID, Fence: fence, Base: run.Pins.Base, ReservationGeneration: run.Pins.Output.ReservationGeneration, Entries: []evolution.PatchEntry{}}, Artifacts: []PatchArtifact{}}
	if !manifest.ValidID(grant) || !manifest.ValidID(run.GroupID) || run.Pins.SourcePolicyRevision < 1 || run.Budget.MaxArtifactBytes < 1 || len(candidate.Files) > 100 {
		return result, fmt.Errorf("invalid candidate scope or budget")
	}
	if run.Pins.Base.TreeDigest != manifest.TreeDigest(base) {
		return result, fmt.Errorf("candidate base mismatch")
	}
	tree := map[string]manifest.Entry{}
	for _, entry := range base {
		tree[entry.Path] = entry
	}
	directories := map[string]bool{}
	files := map[string]bool{}
	total := int64(0)
	for _, file := range candidate.Files {
		if manifest.ValidAuthoringPath(file.Path, false) != nil {
			return result, fmt.Errorf("invalid relative candidate path")
		}
		destination := run.Pins.Output.Prefix + "/" + file.Path
		if !evolution.WithinPrefix(run.Pins.Output.Prefix, destination) || manifest.ValidAuthoringPath(destination, false) != nil || files[destination] {
			return result, fmt.Errorf("duplicate or outside candidate path")
		}
		if existing, ok := tree[destination]; ok && existing.Kind != "file" {
			return result, fmt.Errorf("candidate cannot replace directory with file")
		}
		files[destination] = true
		data := []byte(file.Text)
		size := int64(len(data))
		if size > run.Budget.MaxArtifactBytes-total {
			return result, fmt.Errorf("candidate artifact budget exceeded")
		}
		total += size
		artifact := evolution.ArtifactRef{ID: "file-" + strings.TrimPrefix(evolution.Digest([]byte(run.ID+"\x00"+destination)), "sha256:"), GroupID: run.GroupID, SchemaDigest: evolution.Digest([]byte("example-candidate-text-v1")), Digest: evolution.Digest(data), Size: size, MediaType: "text/markdown; charset=utf-8", GrantID: grant, PolicyRevision: run.Pins.SourcePolicyRevision}
		entry := manifest.Entry{Path: destination, Kind: "file", Size: strconv.FormatInt(size, 10), Digest: artifact.Digest}
		if existing, ok := tree[destination]; ok && existing == entry {
			continue
		}
		result.Artifacts = append(result.Artifacts, PatchArtifact{Reference: artifact, Bytes: data})
		result.Patch.Entries = append(result.Patch.Entries, evolution.PatchEntry{Operation: "put", Path: destination, Kind: "file", Artifact: &artifact})
		for parent := path.Dir(destination); parent != "."; parent = path.Dir(parent) {
			if existing, ok := tree[parent]; ok {
				if existing.Kind != "directory" {
					return result, fmt.Errorf("candidate parent is a file")
				}
				continue
			}
			if !evolution.WithinPrefix(run.Pins.Output.Prefix, parent) {
				return result, fmt.Errorf("output prefix ancestor must already exist")
			}
			directories[parent] = true
		}
	}
	for directory := range directories {
		if files[directory] {
			return result, fmt.Errorf("candidate file/directory collision")
		}
		result.Patch.Entries = append(result.Patch.Entries, evolution.PatchEntry{Operation: "put", Path: directory, Kind: "directory"})
	}
	sort.Slice(result.Patch.Entries, func(i, j int) bool { return result.Patch.Entries[i].Path < result.Patch.Entries[j].Path })
	sort.Slice(result.Artifacts, func(i, j int) bool { return result.Artifacts[i].Reference.ID < result.Artifacts[j].Reference.ID })
	final, err := result.Patch.ApplyTree(run.Pins.Output, base)
	if err != nil {
		return result, err
	}
	if len(result.Patch.Entries) > 0 {
		_, err := manifest.NewProfile("validation", run.Pins.Output.LibraryID, "candidate", run.Pins.Output.Profile, final)
		if err != nil {
			return result, err
		}
		// Existing untouched bytes are verified at Studio intake. New text still gets
		// full schema/profile validation there, after authenticated materialization.
	}
	result.NoChange = manifest.TreeDigest(final) == run.Pins.Base.TreeDigest
	return result, nil
}

// DecodeCandidate requires strict builder output, including unknown-field and
// duplicate-key rejection, before assigning any output prefix or grant.
func DecodeCandidate(raw json.RawMessage) (Candidate, error) {
	return evolution.Decode[Candidate](strings.NewReader(string(raw)))
}
