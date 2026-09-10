self:
{ config, lib, pkgs, ... }:

let
  cfg = config.services.huginn;
  mcp = config.services.huginn-mcp;
  hlib = import ./lib.nix { inherit lib; };
  system = pkgs.stdenv.hostPlatform.system;
  sidecarScript = hlib.mkSidecarScript { inherit pkgs lib; cfg = cfg; };
  mcpScript = hlib.mkMcpScript { inherit pkgs lib; cfg = mcp; };
  zmqListen = "unix:///run/zmqcat/bus.sock";
in
{
  options.services.huginn = import ./sidecar-options.nix { inherit lib; } // {
    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${system}.huginn;
      defaultText = lib.literalExpression "huginn.packages.\${system}.huginn";
      description = "huginn package to run.";
    };
    user = lib.mkOption {
      type = lib.types.str;
      example = "reuben";
      description = ''
        The human whose coding sessions this attaches to. Required: the
        sidecar reads that user's ~/.grok, ~/.claude, and ~/.codex, so
        running it as root or a system user would find nothing.
      '';
    };
  };

  options.services.huginn-mcp = import ./mcp-options.nix { inherit lib; } // {
    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${system}.huginn;
      defaultText = lib.literalExpression "huginn.packages.\${system}.huginn";
      description = "huginn package providing huginn-mcp.";
    };
  };

  config = lib.mkMerge [
    # Independent of huginn / huginn-mcp: join-only machines still need the path.
    (lib.mkIf config.services.zmqcat.enable {
      services.zmqcat.listen = lib.mkDefault zmqListen;
    })
    (lib.mkIf cfg.enable {
      services.huginn.zmqcatListen = lib.mkDefault (
        if config.services.zmqcat.enable then config.services.zmqcat.listen else zmqListen
      );
      assertions = [{
        assertion = cfg.user != "root";
        message = "services.huginn.user must be the human whose sessions it attaches to, not root.";
      }];
      systemd.services.huginn = {
        description = "huginn session sidecar";
        wantedBy = [ "multi-user.target" ];
        after = [ "network-online.target" ];
        wants = [ "network-online.target" ];
        serviceConfig = {
          ExecStart = sidecarScript;
          Restart = "always";
          RestartSec = 2;
          User = cfg.user;
          NoNewPrivileges = true;
        };
      };
    })
    (lib.mkIf mcp.enable {
      services.huginn-mcp.zmqcatListen = lib.mkDefault (
        if config.services.zmqcat.enable then config.services.zmqcat.listen else zmqListen
      );
      systemd.services.huginn-mcp = {
        description = "huginn MCP endpoint";
        wantedBy = [ "multi-user.target" ];
        after = [ "network-online.target" ];
        wants = [ "network-online.target" ];
        serviceConfig = {
          ExecStart = mcpScript;
          Restart = "always";
          RestartSec = 2;
          DynamicUser = true;
          # Read-only fan-out; it needs the bus socket and its token, nothing else.
          NoNewPrivileges = true;
          ProtectSystem = "strict";
          ProtectHome = true;
          PrivateTmp = lib.mkDefault true; # socket is /run/zmqcat, not /tmp
        };
      };
    })
  ];
}
