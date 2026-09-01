{ lib, buildGoModule }:

buildGoModule {
  pname = "huginn";
  version = "0.1.0";

  src = lib.cleanSource ../.;

  # Dependencies are vendored, so the build is hermetic and fetches nothing.
  # Regenerate with `go mod vendor` whenever go.mod changes.
  vendorHash = null;

  subPackages = [ "cmd/huginn" "cmd/huginn-mcp" "cmd/huginn-channel" ];

  ldflags = [ "-s" "-w" ];

  meta = {
    description = "Host sidecar that attaches to live Claude Code, Codex, and Grok Build sessions";
    mainProgram = "huginn";
    platforms = lib.platforms.unix;
  };
}
