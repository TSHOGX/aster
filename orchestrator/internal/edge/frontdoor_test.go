package edge

import (
	"net/http/httptest"
	"testing"
)

func TestFrontDoorRequiresConfiguredHTTPSAndPeer(t *testing.T) {
	for _, f := range []*FrontDoor{{PublicOrigin:"http://example.com",PeerIP:"192.0.2.1"},{PublicOrigin:"https://example.com/path",PeerIP:"192.0.2.1"},{PublicOrigin:"https://example.com",PeerIP:"0.0.0.0"},{PublicOrigin:"https://example.com",PeerIP:"not-an-ip"}} {
		if err:=f.Validate();err==nil{t.Fatalf("accepted %+v",f)}
	}
	f:=&FrontDoor{PublicOrigin:"https://example.com",PeerIP:"192.0.2.1"};if err:=f.Validate();err!=nil{t.Fatal(err)}
	trusted:=httptest.NewRequest("GET","https://example.com/",nil);trusted.RemoteAddr="192.0.2.1:443";trusted.Header.Set("X-Real-IP","198.51.100.4");if !f.accepts(trusted){t.Fatal("trusted peer refused")}
	spoof:=httptest.NewRequest("GET","https://example.com/",nil);spoof.RemoteAddr="198.51.100.8:443";spoof.Header.Set("X-Real-IP","198.51.100.4");if f.accepts(spoof){t.Fatal("untrusted peer accepted")}
	wrongHost:=httptest.NewRequest("GET","https://evil.example/",nil);wrongHost.RemoteAddr="192.0.2.1:443";wrongHost.Header.Set("X-Real-IP","198.51.100.4");if f.accepts(wrongHost){t.Fatal("wrong host accepted")}
	badClient:=httptest.NewRequest("GET","https://example.com/",nil);badClient.RemoteAddr="192.0.2.1:443";badClient.Header.Set("X-Real-IP","not-an-ip");if f.accepts(badClient){t.Fatal("invalid client accepted")}
}
