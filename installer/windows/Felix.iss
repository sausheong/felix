; Felix Windows installer (Inno Setup 6)
;
; Mirrors the macOS PKG installer:
;   - installs felix.exe, felix-app.exe, bundled ollama.exe and skills
;   - on first install, writes ~/.felix/felix.json5 wired to bundled ollama (gemma4)
;   - pre-creates ollama models dir so the in-app downloader can write to it
;   - reminds the user to download the model from Settings → Models
;
; Build:  iscc /DMyAppVersion=0.1.7 Felix.iss     (under wine on macOS or natively on Windows)

#ifndef MyAppVersion
  #define MyAppVersion "0.0.0-dev"
#endif

#define MyAppName "Felix"
#define MyAppPublisher "Sau Sheong Chang"
#define MyAppURL "https://github.com/sausheong/felix"
#define MyAppExeName "felix-app.exe"

[Setup]
; A stable AppId is required so upgrades replace prior installs cleanly.
AppId={{6F1C2A4E-9D1B-4A2D-9A9A-0F2A6F3B9C11}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppVerName={#MyAppName} {#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}
AppUpdatesURL={#MyAppURL}/releases
DefaultDirName={autopf}\Felix
DefaultGroupName=Felix
DisableProgramGroupPage=yes
LicenseFile=
; Output beside Felix.iss; the Makefile moves the .exe to the repo root.
; (Keeps the build portable across wine/docker — docker only mounts this dir.)
OutputDir=.
OutputBaseFilename=Felix-{#MyAppVersion}-windows-amd64
Compression=lzma2
SolidCompression=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=admin
WizardStyle=modern
UninstallDisplayName={#MyAppName} {#MyAppVersion}
UninstallDisplayIcon={app}\{#MyAppExeName}
#ifexist "Felix.ico"
SetupIconFile=Felix.ico
#endif

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "Create a &desktop shortcut"; GroupDescription: "Additional shortcuts:"
Name: "startupicon"; Description: "Start Felix automatically when I log in"; GroupDescription: "Startup:"; Flags: unchecked

[Files]
Source: "payload\felix.exe";          DestDir: "{app}";        Flags: ignoreversion
Source: "payload\felix-app.exe";      DestDir: "{app}";        Flags: ignoreversion
Source: "payload\bin\ollama.exe";     DestDir: "{app}\bin";    Flags: ignoreversion
Source: "payload\skills\*.md";        DestDir: "{app}\skills"; Flags: ignoreversion

[Icons]
Name: "{group}\Felix";            Filename: "{app}\{#MyAppExeName}"
Name: "{group}\Uninstall Felix";  Filename: "{uninstallexe}"
; Admin install → write shortcuts to the All Users profile so every account on
; the box gets them. Avoids the Inno warning about user areas in admin mode.
Name: "{commondesktop}\Felix";    Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon
Name: "{commonstartup}\Felix";    Filename: "{app}\{#MyAppExeName}"; Tasks: startupicon

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "Launch Felix"; Flags: nowait postinstall skipifsilent

[Code]
function FelixDir(): string;
begin
  Result := GetEnv('USERPROFILE') + '\.felix';
end;

function JsonEscape(S: string): string;
begin
  // JSON5 strings — backslashes need doubling, then prefer forward slashes
  // (valid in JSON, avoids the escape entirely on Windows paths).
  Result := S;
  StringChangeEx(Result, '\', '/', True);
end;

procedure CopyBundledSkills();
var
  FindRec: TFindRec;
  SrcDir, DstDir, DstFile: string;
begin
  SrcDir := ExpandConstant('{app}\skills');
  DstDir := FelixDir() + '\skills';
  ForceDirectories(DstDir);

  if FindFirst(SrcDir + '\*.md', FindRec) then
  try
    repeat
      DstFile := DstDir + '\' + FindRec.Name;
      // -n equivalent on macOS: don't overwrite existing user copies
      if not FileExists(DstFile) then
        CopyFile(SrcDir + '\' + FindRec.Name, DstFile, False);
    until not FindNext(FindRec);
  finally
    FindClose(FindRec);
  end;
end;

procedure WriteBundledOllamaConfig();
var
  Cfg: string;
  Lines: TStringList;
  Workspace: string;
begin
  Cfg := FelixDir() + '\felix.json5';
  if FileExists(Cfg) then
    exit;

  Workspace := JsonEscape(FelixDir() + '\workspace-default');

  Lines := TStringList.Create;
  try
    Lines.Add('{');
    Lines.Add('  "agents": {');
    Lines.Add('    "list": [{');
    Lines.Add('      "id": "default",');
    Lines.Add('      "name": "Assistant",');
    Lines.Add('      "workspace": "' + Workspace + '",');
    Lines.Add('      "model": "local/gemma4",');
    Lines.Add('      "sandbox": "none",');
    Lines.Add('      "tools": { "allow": ["read_file","write_file","edit_file","bash","web_fetch","web_search","browser","send_message","cron"] }');
    Lines.Add('    }]');
    Lines.Add('  },');
    Lines.Add('  "bindings": [{ "agentId": "default", "match": { "channel": "cli" } }],');
    Lines.Add('  "channels": { "cli": { "enabled": true, "interactive": true } },');
    Lines.Add('  "local": { "enabled": true, "keep_alive": "5m" },');
    Lines.Add('  "providers": {');
    Lines.Add('    "local": { "kind": "local", "base_url": "http://127.0.0.1:18790/v1" }');
    Lines.Add('  }');
    Lines.Add('}');
    Lines.SaveToFile(Cfg);
  finally
    Lines.Free;
  end;
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
  begin
    ForceDirectories(FelixDir());
    ForceDirectories(FelixDir() + '\ollama\models');
    CopyBundledSkills();
    WriteBundledOllamaConfig();

    MsgBox(
      'Felix is configured to use gemma4 but the model isn''t downloaded yet.' + #13#10 + #13#10 +
      'After Felix opens, go to Settings -> Models and click Download next to gemma4. ' +
      'The download runs in the background and Felix will use the model as soon as it''s ready.',
      mbInformation, MB_OK);
  end;
end;
