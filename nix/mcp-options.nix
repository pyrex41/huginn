# Options for the orchestration-side MCP endpoint.
{ lib }:

with lib;
{
  enable = mkEnableOption "the huginn MCP endpoint";

  package = mkOption {
    type = types.package;
    description = "huginn package providing huginn-mcp.";
  };

  bind = mkOption {
    type = types.str;
    default = "127.0.0.1:7420";
    description = ''
      HTTP listen address. Put a TLS terminator or an overlay in front
      before exposing this beyond loopback.
    '';
  };

  tokenFile = mkOption {
    type = types.path;
    example = "/run/secrets/huginn-mcp-token";
    description = ''
      File holding the bearer token every harness must present. A path,
      never a literal.
    '';
  };

  zmqcatListen = mkOption {
    type = types.str;
    default = "unix:///tmp/zmqcat.sock";
    description = "zmqcat bus socket to fan out over.";
  };

  timeout = mkOption {
    type = types.str;
    default = "30s";
    description = "Per-machine request timeout. Raise it if you add slow verbs later.";
  };

  staleAfter = mkOption {
    type = types.str;
    default = "45s";
    description = "Drop a machine from the roster after this long without an announcement.";
  };

  extraArgs = mkOption {
    type = types.listOf types.str;
    default = [ ];
    description = "Additional huginn-mcp arguments.";
  };
}
