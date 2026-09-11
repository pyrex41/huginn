# Options for the sidecar, shared by home-manager and NixOS.
{ lib }:

with lib;
{
  enable = mkEnableOption "the huginn sidecar";

  package = mkOption {
    type = types.package;
    description = "huginn package to run.";
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
    description = ''
      Listen address. Loopback by default. A private overlay IP
      (WireGuard, Tailscale 100.64/10) shares the sidecar; 0.0.0.0 is refused.
    '';
  };

  extraArgs = mkOption {
    type = types.listOf types.str;
    default = [ ];
    description = "Additional huginn serve arguments.";
  };
}
