package discovery

import "testing"

func TestParseCIDRs(t *testing.T) {
	got, err := ParseCIDRs("192.168.1.8/24, 10.0.0.1/16")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].String() != "192.168.1.0/24" || got[1].String() != "10.0.0.0/16" {
		t.Fatalf("got %v", got)
	}
}

func TestParseCIDRsRejeitaLixo(t *testing.T) {
	if _, err := ParseCIDRs("rede-de-casa"); err == nil {
		t.Fatal("esperava erro")
	}
}

func TestParsePorts(t *testing.T) {
	got, err := ParsePorts("554, 8554")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 554 || got[1] != 8554 {
		t.Fatalf("got %v", got)
	}
	if _, err := ParsePorts("0"); err == nil {
		t.Fatal("porta 0 deveria ser inválida")
	}
}
