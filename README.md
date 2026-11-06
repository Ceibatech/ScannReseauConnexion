# ScannReseauConnexion — poste 3 MULCV

Numérisation depuis le **scanner réseau HP LaserJet MFP M236dw** (`NPI3F8C0F`) et dépôt
automatique dans **MULCV**. C'est le **poste 3**, construit sur la même logique que les
postes PaperStream 1 et 2 ([Ceibatech/PaperStream](https://github.com/Ceibatech/PaperStream)) :
même arborescence `C:\MULCV`, même compte technique Keycloak par poste, même API
`/intake/scans`, mêmes règles de QR (carton, dossier, séparateur de pièce).

La différence : ici il n'y a pas de PaperStream. Le programme **pilote lui-même le scanner
par le réseau** (protocole eSCL/AirScan, celui de Mopria et AirPrint, intégré aux HP), récupère
chaque page, assemble le lot en un PDF dans l'ordre de numérisation et l'envoie à MULCV.
Rien n'est à installer côté imprimante : il suffit qu'elle soit sur le même réseau que le PC.

## Installer le poste

1. Le PC doit voir l'imprimante dans **Paramètres > Bluetooth et appareils > Imprimantes et
   scanners** (`NPI3F8C0F (HP LaserJet MFP M236dw)`).
2. Cloner le dépôt **n'importe où** :
   ```
   git clone https://github.com/Ceibatech/ScannReseauConnexion.git
   ```
3. Obtenir le **secret du poste** auprès de l'administrateur (voir *Administration*) :
   `cat /opt/ceiba/keycloak/secrets/mulcv-depot-poste3.secret`
4. Double-cliquer sur **`installer.cmd`**. Il propose l'adresse du scanner (l'IP du port de
   l'imprimante associée au PC, sinon `NPI3F8C0F`) : Entrée pour l'accepter. Coller le secret.
   La fenêtre doit finir par **« Connexion à MULCV et au scanner vérifiées »** et un raccourci
   **Numeriser MULCV** apparaît sur le Bureau.
5. Diagnostic à tout moment : `C:\MULCV\tester-connexion.cmd`.

> Conseil réseau : réserver l'adresse IP de l'imprimante dans la box/le routeur (bail DHCP
> fixe). Sinon garder le nom `NPI3F8C0F`, résolu automatiquement par Windows.

## Numériser

Double-clic sur **Numeriser MULCV** :

```
Lot : 0 page(s), 0.0 Mo.   [Entrée] numériser   [T] terminer et envoyer   [A] annuler le lot   [Q] quitter
```

- Poser la feuille sur la vitre, **Entrée** : la page est ajoutée au lot. Recommencer pour
  chaque feuille, dans l'ordre. (Si un modèle avec chargeur est utilisé et qu'il est garni,
  tout le chargeur est numérisé d'un coup.)
- **T** : le lot devient un seul PDF `LOT-<date>-<heure>.pdf`, envoyé aussitôt à MULCV.
- **A** : annule le lot ; les pages sont mises de côté dans `3-erreurs\lot-abandonne-…`,
  jamais effacées.
- **Q** : quitte. Un lot non envoyé est conservé et proposé au lancement suivant (chaque page
  est écrite dans `numerisation\0-en-cours` dès sa réception : une coupure ne perd rien).

### Ordre des feuilles (identique aux postes 1 et 2)

```
QR CARTON   2010-KOU-0201          ← fiche du carton (conseillée)
QR DOSSIER  2010-KOU-0201-001      ← 1re feuille du dossier
  pièces du dossier…
QR DOSSIER  2010-KOU-0201-002
  pièce 1
SÉPARATEUR  MULCV-PIECE            ← entre deux pièces d'un même dossier
  pièce 2
```

MULCV lit le QR de chaque page et découpe lui-même : un lot peut contenir tout un carton.
Le séparateur de pièce à imprimer est `separateur-de-piece-MULCV.pdf`. Le lot est refusé
(dans `3-erreurs`, avec la raison et le numéro de page) si une étiquette est inconnue, si des
pages précèdent la première étiquette, ou si un dossier n'appartient pas au carton scanné.

**Taille** : 25 Mo au plus par lot (≈ 50 à 80 pages en niveaux de gris à 300 ppp). Le
programme prévient avant la limite : envoyer le lot (T) et continuer dans un nouveau lot,
MULCV rattache les pages suivantes au dossier en cours. Le plus simple : un lot par dossier
ou par carton.

## Ce que l'installateur crée : `C:\MULCV`

```
C:\MULCV\
├── numeriser.cmd               ← double-clic (ou raccourci du Bureau) : numériser
├── tester-connexion.cmd        ← vérifie MULCV et le scanner
├── tester-envoi.cmd            ← envoie un PDF existant, sans scanner
├── separateur-de-piece-MULCV.pdf
├── bin\mulcv-scan.exe
├── config\
│   ├── postes\poste-3.json     ← modèle
│   └── mulcv-scan.json         ← configuration du poste, avec le secret (lisible par l'opérateur seul)
├── numerisation\
│   ├── 0-en-cours\             ← pages du lot en cours (reprise après coupure)
│   ├── 1-a-envoyer\            ← lots en attente si MULCV ou le réseau est indisponible
│   ├── 2-envoyes\              ← lots bien arrivés dans MULCV (copie locale)
│   └── 3-erreurs\              ← lots refusés + un .txt qui explique pourquoi
└── journal\mulcv-scan.log      ← une ligne par lot
```

## Réglages du scanner (`config\mulcv-scan.json`, bloc `scanner`)

| Clé | Défaut | Rôle |
|---|---|---|
| `url` | `NPI3F8C0F` | nom réseau, IP ou URL (`http://…/eSCL` est complété tout seul) |
| `resolution` | `300` | ppp ; 300 est le bon compromis OCR / lecture des QR / taille |
| `colorMode` | `Grayscale8` | ou `RGB24`. **Mode fixe**, comme exigé pour PaperStream |
| `paper` | `A4` | `A4`, `Letter`, `Legal` (borné à la taille maximale du scanner) |
| `source` | `auto` | `auto` (chargeur s'il est garni, sinon vitre), `Platen`, `Feeder` |
| `insecureTls` | `false` | accepter le certificat auto-signé si l'URL est en `https` |

Modifier ce fichier puis lancer `tester-connexion.cmd`.

## D'où ça part, où ça arrive

1. `numeriser.cmd` lance `bin\mulcv-scan.exe -scan`, qui demande ses capacités au scanner
   (`GET /eSCL/ScannerCapabilities`).
2. À chaque Entrée : `POST /eSCL/ScanJobs` (vitre ou chargeur, niveaux de gris, 300 ppp, A4)
   puis `GET …/NextDocument` jusqu'à la dernière page, en JPEG.
3. À **T** : les JPEG sont assemblés en PDF **sans réencodage** (aucune perte pour l'OCR),
   déposé dans `numerisation\1-a-envoyer`.
4. Le programme s'identifie auprès de Keycloak avec `mulcv-depot-poste3` et envoie le PDF à
   `POST /api/v1/intake/scans` ; MULCV lit les QR et range dans
   **`Scan / Production / <zone> / <conteneur> / <carton> / <dossier>`**.
5. Le PDF passe dans `2-envoyes`, ou `3-erreurs` avec la raison. Si MULCV ou le réseau est
   indisponible, il reste dans `1-a-envoyer` et repart au lancement suivant.

### Variante sans pilotage (dossier surveillé)

Si l'on préfère lancer les numérisations depuis l'écran de l'imprimante ou HP Smart, les
enregistrer en PDF dans `C:\MULCV\numerisation\1-a-envoyer` et installer avec la surveillance :
`powershell -ExecutionPolicy Bypass -File install.ps1 -Surveillance`. Chaque PDF déposé est
envoyé dès que sa taille ne bouge plus.

## Dépannage

| Message | Cause probable |
|---|---|
| `Scanner injoignable` | imprimante éteinte ou en veille profonde, autre réseau (Wi-Fi invité), IP changée : la réserver ou repasser au nom `NPI3F8C0F` |
| `ScannerCapabilities : HTTP 404` | eSCL désactivé : serveur web intégré de l'imprimante (taper son IP dans un navigateur) > Réseau > AirPrint / Mopria : activer la numérisation |
| `scanner occupé` | une copie ou une numérisation lancée depuis l'écran est en cours |
| `refusé par MULCV (422) … QR` | étiquette absente de la 1re page, coupée ou mal posée sur la vitre |

## Administration et développement

- `admin\keycloak\mulcv-depot-poste3.client.json` : compte technique du poste, sur le modèle
  des postes 1 et 2. À **ajouter** à `provision_intake_clients.py` (dépôt
  `MULCV-search-service`), puis rendre le compte **Collaborateur** du site MULCV dans Share.
  Le secret est ensuite dans `/opt/ceiba/keycloak/secrets/mulcv-depot-poste3.secret`.
- `./build.sh` : tests et reconstruction de `poste\bin\mulcv-scan.exe` (Go 1.24.7). La CI
  refuse un exécutable qui ne correspond pas exactement aux sources de `src\`.
- `src\main.go` (dépôt MULCV, journal, verrou d'envoi : repris de PaperStream),
  `src\escl.go` (pilotage du scanner), `src\pdf.go` (assemblage), `src\scan.go` (interface).
  Les tests simulent le scanner eSCL et MULCV.
- Contrat de l'API : `docs/transfer-api.md` du dépôt `MULCV-search-service`.
