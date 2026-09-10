self:
{ config, lib, pkgs, ... }:

let
  mcp = config.services.huginn-mcp;
  hlib = import ./lib.nix { inherit lib; };
  system = pkgs.stdenv.hostPlatform.system;
  mcpScript = hlib.mkMcpScript { inherit pkgs lib; cfg = mcp; };
  zmqListen = "unix:///var/lib/zmqcat/bus.sock";
in
{
  # Only the MCP endpoint is a system daemon here. The sidecar belongs to a
  # user (it reads their ~/.claude and friends), so it ships as a
  # home-manager launchd agent instead.
  options.services.huginn-mcp = import ./mcp-options.nix {
    inherit lib;
    zmqcatListenDefault = zmqListen;
  } // {
    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${system}.huginn;
      defaultText = lib.literalExpression "huginn.packages.\${system}.huginn";
      description = "huginn package providing huginn-mcp.";
    };
    logFile = lib.mkOption {
      type = lib.types.str;
      default = "/var/log/huginn-mcp.log";
      description = "Where launchd writes stdout and stderr.";
    };
  };

  config = lib.mkMerge [
    # Independent of huginn-mcp: join-only machines still need the path.
    (lib.mkIf config.services.zmqcat.enable {
      services.zmqcat.listen = lib.mkDefault zmqListen;
      # Root launchd zmqcat; user sidecar must traverse. 0700 would hide the socket.
      system.activationScripts.extraActivation.text = lib.mkAfter ''
        mkdir -p /var/lib/zmqcat
        chmod 0755 /var/lib/zmqcat
      '';
    })
    (lib.mkIf mcp.enable {
      services.huginn-mcp.zmqcatListen = lib.mkDefault (
        if config.services.zmqcat.enable then config.services.zmqcat.listen else zmqListen
      );
      launchd.daemons.huginn-mcp = {
        script = "exec ${mcpScript}";
        serviceConfig = {
          Label = "org.nixos.huginn-mcp";
          RunAtLoad = true;
          KeepAlive = true;
          StandardOutPath = mcp.logFile;
          StandardErrorPath = mcp.logFile;
        };
      };
    })
  ];
}
