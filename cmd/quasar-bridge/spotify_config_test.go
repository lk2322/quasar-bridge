package main

import "testing"

func TestSpotifyOnlyHybridNeedsNoModel(t *testing.T) {
	cfg, err := parseConfig([]byte(`{"mode":"hybrid","spotify":{"enabled":true,"device":"Мини 3"}}`), false)
	if err != nil {
		t.Fatalf("Spotify-only hybrid rejected: %v", err)
	}
	if cfg.Spotify.Mode != "replace" || cfg.Spotify.TokenFile != "spotify-token.json" || cfg.Spotify.ActiveMinutes != 60 {
		t.Errorf("Spotify defaults lost: %+v", cfg.Spotify)
	}
}

func TestHybridWithoutSpotifyStillNeedsModel(t *testing.T) {
	if _, err := parseConfig([]byte(`{"mode":"hybrid"}`), false); err == nil {
		t.Fatal("hybrid without a model and without Spotify accepted")
	}
	// A configured model still needs its name, Spotify or not.
	if _, err := parseConfig([]byte(`{"mode":"hybrid","llm_base_url":"http://127.0.0.1:1/v1","spotify":{"enabled":true,"device":"x"}}`), false); err == nil {
		t.Fatal("LLM URL without a model accepted")
	}
}

func TestSpotifyConfigValidated(t *testing.T) {
	if _, err := parseConfig([]byte(`{"mode":"hybrid","spotify":{"enabled":true}}`), false); err == nil {
		t.Fatal("enabled Spotify without a device accepted")
	}
	if _, err := parseConfig([]byte(`{"spotify":{"enabled":true,"device":"x","mode":"always"}}`), false); err == nil {
		t.Fatal("unknown Spotify mode accepted")
	}
}
