{ lib }:

{
  # mkSidecarArgs builds `huginn serve` flags from module options.
  mkSidecarArgs = cfg:
    [ "serve" "--bind" cfg.bind "--zmqcat" ]
    ++ [ "--zmqcat-listen" cfg.zmqcatListen ]
    ++ [ "--zmqcat-service" cfg.service ]
    ++ [ "--zmqcat-workers" (toString cfg.workers) ]
    ++ lib.optionals (!cfg.presence) [ "--zmqcat-no-presence" ]
    ++ lib.optionals cfg.presence [ "--zmqcat-presence-every" cfg.presenceEvery ]
    ++ cfg.extraArgs;

  mkMcpArgs = cfg:
    [ "--bind" cfg.bind ]
    ++ [ "--zmqcat-listen" cfg.zmqcatListen ]
    ++ [ "--timeout" cfg.timeout ]
    ++ [ "--stale-after" cfg.staleAfter ]
    ++ cfg.extraArgs;

  mkMcpScript = { pkgs, lib, cfg }:
    pkgs.writeShellScript "huginn-mcp-start" ''
      set -eu
      if [ ! -r ${lib.escapeShellArg (toString cfg.tokenFile)} ]; then
        echo "huginn-mcp: cannot read tokenFile ${toString cfg.tokenFile}" >&2
        exit 1
      fi
      HUGINN_MCP_TOKEN="$(cat ${lib.escapeShellArg (toString cfg.tokenFile)})"
      export HUGINN_MCP_TOKEN
      exec ${cfg.package}/bin/huginn-mcp ${lib.escapeShellArgs (
        (import ./lib.nix { inherit lib; }).mkMcpArgs cfg
      )}
    '';

  # The token has to reach the process as an environment variable without
  # ever being a Nix store path, so every backend wraps the binary the same
  # way rather than each inventing its own.
  mkSidecarScript = { pkgs, lib, cfg }:
    pkgs.writeShellScript "huginn-sidecar" ''
      set -eu
      if [ ! -r ${lib.escapeShellArg (toString cfg.tokenFile)} ]; then
        echo "huginn: cannot read tokenFile ${toString cfg.tokenFile}" >&2
        exit 1
      fi
      HUGINN_TOKEN="$(cat ${lib.escapeShellArg (toString cfg.tokenFile)})"
      export HUGINN_TOKEN
      exec ${lib.getExe cfg.package} ${lib.escapeShellArgs (
        (import ./lib.nix { inherit lib; }).mkSidecarArgs cfg
      )}
    '';
}
