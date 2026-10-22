package evolution

import "testing"

func TestDefinitionDigestBindsPayloadWithoutCircularHash(t *testing.T) {
	service := Service{Ref: Revision{ID: "service", Revision: 1}, Kind: "evolution", ConnectionID: "connection", RunnerDigest: Digest([]byte("runner")), Capabilities: Capabilities{ProtocolVersion: 1}}
	digest, err := DefinitionDigest(service)
	if err != nil {
		t.Fatal(err)
	}
	service.Ref.Digest = digest
	again, err := DefinitionDigest(service)
	if err != nil || again != digest {
		t.Fatal("self digest changed hash", err)
	}
	for _, change := range []func(*Service){func(s *Service) { s.Ref.ID = "other" }, func(s *Service) { s.Ref.Revision++ }, func(s *Service) { s.ConnectionID = "other" }, func(s *Service) { s.RunnerDigest = Digest([]byte("changed")) }} {
		changed := service
		change(&changed)
		actual, err := DefinitionDigest(changed)
		if err != nil || actual == digest {
			t.Fatal("payload substitution did not change digest", err)
		}
	}
}
