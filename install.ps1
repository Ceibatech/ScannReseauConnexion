<#
.SYNOPSIS
  Installe ou met a jour le poste 3 MULCV : numerisation depuis le scanner reseau
  HP LaserJet MFP M236dw (NPI3F8C0F) et depot automatique dans MULCV.

.DESCRIPTION
  Cree C:\MULCV et toute son arborescence (bin, config, numerisation, journal),
  y copie le programme, ecrit la configuration du poste (secret + adresse du scanner),
  verifie la connexion a MULCV et au scanner, puis pose un raccourci
  "Numeriser MULCV" sur le Bureau.
  Relancer apres un "git pull" met a jour le programme sans toucher a la configuration
  ni aux fichiers numerises.

.EXAMPLE
  installer.cmd                (double-clic)
  powershell -ExecutionPolicy Bypass -File install.ps1 -Scanner 192.168.1.40 -Surveillance

.NOTES
  A lancer avec le compte Windows de l'operateur du poste.
  Le secret du poste se lit sur le serveur :
    cat /opt/ceiba/keycloak/secrets/mulcv-depot-poste3.secret
#>
param(
  [string]$Destination = 'C:\MULCV',
  # Adresse du scanner (nom reseau, IP ou URL). Par defaut : celle de l'imprimante associee a ce PC.
  [string]$Scanner = '',
  # Tache planifiee qui envoie ce qui est depose dans numerisation\1-a-envoyer (scan vers dossier, HP Smart...).
  [switch]$Surveillance
)
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
$kit = Join-Path (Split-Path -Parent $MyInvocation.MyCommand.Path) 'poste'
if (-not (Test-Path (Join-Path $kit 'bin\mulcv-scan.exe'))) { throw "poste\bin\mulcv-scan.exe introuvable : cloner le depot complet." }

# Adresse de l'imprimante associee a ce PC (Parametres > Imprimantes et scanners).
# Un port TCP/IP donne l'adresse IP ; un port WSD n'en donne pas : on garde alors le nom reseau.
function Find-Scanner {
  try {
    $imprimantes = Get-Printer -ErrorAction Stop | Where-Object { $_.Name -match 'NPI3F8C0F|M23[2-7]' -or $_.DriverName -match 'M23[2-7]' }
    foreach ($imprimante in $imprimantes) {
      $port = Get-PrinterPort -Name $imprimante.PortName -ErrorAction SilentlyContinue
      if ($port -and $port.PrinterHostAddress) { return $port.PrinterHostAddress }
    }
  } catch { }
  return 'NPI3F8C0F'
}

# 1. Arborescence et programme : tout est cree s'il manque, rien de l'utilisateur n'est efface.
foreach ($d in @('bin', 'config\postes', 'numerisation\0-en-cours', 'numerisation\1-a-envoyer', 'numerisation\2-envoyes', 'numerisation\3-erreurs', 'journal')) {
  New-Item -ItemType Directory -Force -Path (Join-Path $Destination $d) | Out-Null
}
Get-ChildItem $kit -Recurse -File | ForEach-Object {
  $relatif = $_.FullName.Substring($kit.Length + 1)
  Copy-Item $_.FullName (Join-Path $Destination $relatif) -Force
}
$exe = Join-Path $Destination 'bin\mulcv-scan.exe'
$cible = Join-Path $Destination 'config\mulcv-scan.json'
$modele = Join-Path $Destination 'config\postes\poste-3.json'
Write-Host "Arborescence prete dans $Destination."

# 2. Configuration du poste (secret et adresse du scanner demandes a la premiere installation).
$garder = $false
if (Test-Path $cible) {
  $actuel = Get-Content $cible -Raw | ConvertFrom-Json
  $reponse = Read-Host "Ce poste est deja configure ($($actuel.clientId), scanner $($actuel.scanner.url)). Garder cette configuration ? [O/n]"
  $garder = ($reponse -eq '' -or $reponse -match '^[oOyY]')
}
if (-not $garder) {
  $config = Get-Content $modele -Raw | ConvertFrom-Json
  if ([string]::IsNullOrWhiteSpace($Scanner)) {
    $propose = Find-Scanner
    $saisie = Read-Host "Adresse du scanner reseau [$propose]"
    $Scanner = if ([string]::IsNullOrWhiteSpace($saisie)) { $propose } else { $saisie.Trim() }
  }
  $config.scanner.url = $Scanner
  $secure = Read-Host "Secret Keycloak du client $($config.clientId)" -AsSecureString
  $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
  try { $secret = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr) }
  finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
  if ([string]::IsNullOrWhiteSpace($secret)) { throw "Secret vide" }
  $config | Add-Member -NotePropertyName clientSecret -NotePropertyValue $secret.Trim() -Force
  $config | ConvertTo-Json -Depth 4 | Set-Content -Path $cible -Encoding ASCII
  Remove-Variable secret

  # Le secret n'est lisible que par l'operateur, SYSTEM et les administrateurs (SID : independant de la langue).
  $acl = New-Object Security.AccessControl.FileSecurity
  $acl.SetAccessRuleProtection($true, $false)
  $comptes = @(
    [Security.Principal.WindowsIdentity]::GetCurrent().User,
    (New-Object Security.Principal.SecurityIdentifier 'S-1-5-18'),
    (New-Object Security.Principal.SecurityIdentifier 'S-1-5-32-544'))
  foreach ($compte in $comptes) {
    $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($compte, 'FullControl', 'Allow')))
  }
  Set-Acl -Path $cible -AclObject $acl
}

# 3. Verification de la connexion a MULCV puis au scanner.
& $exe -test
if ($LASTEXITCODE -ne 0) {
  throw "Le test a echoue (voir le message ci-dessus) : verifier le secret, la connexion Internet, et que l'imprimante est allumee sur le meme reseau. Relancer installer.cmd en repondant n pour corriger."
}

# 4. Raccourci "Numeriser MULCV" sur le Bureau de l'operateur.
$raccourci = (New-Object -ComObject WScript.Shell).CreateShortcut((Join-Path ([Environment]::GetFolderPath('Desktop')) 'Numeriser MULCV.lnk'))
$raccourci.TargetPath = Join-Path $Destination 'numeriser.cmd'
$raccourci.WorkingDirectory = $Destination
$raccourci.Description = 'Numeriser sur le scanner reseau et deposer dans MULCV (poste 3)'
$raccourci.Save()

if ($Surveillance) {
  $action = New-ScheduledTaskAction -Execute $exe -Argument '-watch' -WorkingDirectory $Destination
  $declencheur = New-ScheduledTaskTrigger -AtLogOn -User ([Security.Principal.WindowsIdentity]::GetCurrent().Name)
  $reglages = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
  Register-ScheduledTask -TaskName 'MULCV depot scanner reseau' -Action $action -Trigger $declencheur -Settings $reglages -Force | Out-Null
  Start-ScheduledTask -TaskName 'MULCV depot scanner reseau'
  Write-Host "Surveillance active sur $Destination\numerisation\1-a-envoyer."
}

Write-Host ''
Write-Host "Poste 3 installe dans $Destination. Connexion a MULCV et au scanner verifiees."
Write-Host ''
Write-Host 'Pour numeriser : double-clic sur "Numeriser MULCV" (Bureau) ou sur'
Write-Host "  $Destination\numeriser.cmd"
Write-Host "Essai sans scanner : $Destination\tester-envoi.cmd"
