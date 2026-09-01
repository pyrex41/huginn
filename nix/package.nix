{ lib, buildGoModule }:

buildGoModule {
  pname = "huginn";
  version = "0.1.0";

  src = lib.cleanSource ../.;

  # Dependencies are vendored. huginn depends on github.com/pyrex41/zmqcat,
  # which is private; vendoring means `nix build` needs no credentials for
  # dependencies and the build is hermetic. Regenerate with `go mod vendor`
  # whenever go.mod changes.
  vendorHash = null;

  subPackages = [ "cmd/huginn" "cmd/huginn-mcp" "cmd/huginn-channel" ];

  ldflags = [ "-s" "-w" ];

  meta = {
    description = "Host sidecar that attaches to live Claude Code, Codex, and Grok Build sessions";
    mainProgram = "huginn";
    platforms = lib.platforms.unix;
  };
}
