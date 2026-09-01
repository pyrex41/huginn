# Options for the machine-side sidecar, shared by the home-manager, NixOS,
# and nix-darwin modules so they cannot drift.
{ lib }:

with lib;
{
  enable = mkEnableOption "the huginn sidecar";

  package = mkOption {
    type = types.package;
    description = "huginn package to run.";
  };

  service = mkOption {
    type = types.str;
    example = "h.studio";
    description = ''
      This machine's name on the bus. Callers address it by this, and it is
      the mailbox huginn serves — so it must be unique across the bus.
    '';
  };

  tokenFile = mkOption {
    type = types.path;
    example = "/run/secrets/huginn-token";
    description = ''
      File holding HUGINN_TOKEN. A path, never a literal: anything inline
      lands in the world-readable Nix store.
    '';
  };

  bind = mkOption {
    type = types.str;
    default = "127.0.0.1:7419";
    description = "Loopback address for the HTTP JSON-RPC surface.";
  };

  zmqcatListen = mkOption {
    type = types.str;
    default = "unix:///tmp/zmqcat.sock";
    description = "Local zmqcat sidecar socket to attach to.";
  };

  workers = mkOption {
    type = types.ints.positive;
    default = 4;
    description = ''
      Concurrent zmqcat READY workers. Each holds its own session; one
      worker would queue every caller behind one slow session/prompt.
    '';
  };

  presence = mkOption {
    type = types.bool;
    default = true;
    description = "Announce this machine on huginn.presence.<service>.";
  };

  presenceEvery = mkOption {
    type = types.str;
    default = "15s";
    description = "Presence announcement interval.";
  };

  extraArgs = mkOption {
    type = types.listOf types.str;
    default = [ ];
    description = "Additional huginn serve arguments.";
  };
}
