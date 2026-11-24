# SharePoint Arkiv

Et baggrundsprogram til Windows (x64), der kopierer filer fra en lokal mappe til en mappe i SharePoint Online. Programmet behandler også filer, der allerede ligger i kildemappen. Det venter som standard to minutter efter en fils seneste ændring, så filer i brug får tid til at blive skrevet færdige.

Når lokal sletning er slået til, slettes en fil permanent, hvis dens seneste ændring er mere end 30 døgn gammel, og programmet har bekræftet, at SharePoint-kopien har samme indhold. En fil, der er låst til skrivning, beholdes og prøves igen. Ved en navnekonflikt med forskelligt indhold beholdes begge filer, og konflikten skrives i loggen. Programmet arbejder med filer direkte i kildemappen. Det kopierer kun til den valgte destination og ændrer eller sletter ingen eksisterende SharePoint-filer.

## 1. Åbn appen

1. Pak ZIP-filen ud i en skrivbar mappe uden for kildemappen. Brug en mappe, hvor kun administratorer og den konto, der kører appen, har adgang.
2. Dobbeltklik på **SharePoint-Arkiv.exe**. Brug **Gennemse** til at vælge den lokale kildemappe. Indtast SharePoint-webstedets adresse, dokumentbibliotekets visningsnavn og mappens sti i biblioteket, fx `Arkiv/Eksport`. Et tomt mappefelt vælger bibliotekets rod.
3. Klik **Opret certifikat**. Knappen **Vis certifikat** finder den offentlige fil **klient.cer**. Den private nøgle bliver i programmappen. Certifikatet er gyldigt i ét år.

Ved opgradering kan du stoppe en eventuel aktiv kørsel og erstatte programfilen i den gamle programmappe. Vinduet indlæser eksisterende indstillinger og bruger det eksisterende certifikat.

## 2. Giv programmet adgang til SharePoint

1. Opret en **single-tenant** appregistrering i Microsoft Entra. Gem **Directory (tenant) ID** og **Application (client) ID**.
2. Upload **klient.cer** under **Certificates & secrets → Certificates**.
3. Tilføj **Microsoft Graph → Application permissions → Sites.Selected**, og giv administratorsamtykke.
4. Tildel appen rollen **write** på det valgte websted. Nedenstående eksempel køres af en administrator med de nødvendige rettigheder. Udfyld værtsnavn, webstedssti og klient-id.

```powershell
Install-Module Microsoft.Graph.Authentication -Scope CurrentUser
Connect-MgGraph -Scopes 'Sites.FullControl.All'

$spHost = 'firma.sharepoint.com'
$sitePath = '/sites/Drift'
$clientId = '00000000-0000-0000-0000-000000000000'

$site = Invoke-MgGraphRequest -Method GET -Uri "https://graph.microsoft.com/v1.0/sites/${spHost}:${sitePath}"
$permission = @{
    roles = @('write')
    grantedToIdentities = @(
        @{ application = @{ id = $clientId; displayName = 'SharePoint Arkiv' } }
    )
}
Invoke-MgGraphRequest -Method POST `
    -Uri "https://graph.microsoft.com/v1.0/sites/$($site.id)/permissions" `
    -ContentType 'application/json' `
    -Body ($permission | ConvertTo-Json -Depth 5)
```

[Microsofts vejledning til Sites.Selected](https://learn.microsoft.com/en-us/graph/permissions-selected-overview) · [Tildeling af adgang til et websted](https://learn.microsoft.com/en-us/graph/api/site-post-permissions?view=graph-rest-1.0)

## 3. Gem, test og start

1. Indtast **Lejer-id** og **Program-id** fra appregistreringen, og klik **Gem indstillinger**.
2. Klik **Test forbindelse**. Statusfeltet viser, hvilken SharePoint-mappe appen kan læse. Brug **Biblioteks-id** med bibliotekets Graph-id, hvis opslag på visningsnavnet fejler. En administrator med læseadgang kan finde id'et med `GET /sites/{site-id}/drives`. [Microsofts vejledning til biblioteksopslag](https://learn.microsoft.com/en-us/graph/api/drive-list?view=graph-rest-1.0)
3. Klik **Prøvekør kopiering** for at overføre filer uden lokal sletning. Knappen **Åbn log** viser resultatet i **arkiv.log**.
4. Sæt flueben ved **Slet verificerede lokale filer efter 30 døgn**, hvis oprydningen skal være aktiv. Klik **Start overvågning**. Appen gemmer indstillingerne og tjekker som standard kilden hvert femte minut, så længe vinduet er åbent. Klik **Stop** for at afbryde overvågningen.

## 4. Kør automatisk

Brug **Opgavestyring**, hvis overvågningen skal fortsætte, når vinduet er lukket. Gem først indstillingerne i appen.

- Under **Generelt** vælg at køre, uanset om brugeren er logget på. Brug en konto med læseadgang til kilden, sletteadgang når oprydning er aktiv, læseadgang til certifikat og nøgle samt skriveadgang til loggen.
- Under **Udløsere** vælg **Ved start**, så programmet starter igen efter en genstart af Windows.
- Under **Handlinger** angiv den fulde sti til **SharePoint-Arkiv.exe**, og skriv `-watch` i argumentfeltet.
- Under **Indstillinger** fjern den automatiske tidsgrænse, og vælg at springe en ny kørsel over, hvis opgaven allerede kører. Vælg at genstarte opgaven ved fejl. Kør opgaven manuelt første gang, og kontrollér **arkiv.log**.

Genstart den planlagte opgave efter ændringer i appen. Til en enkelt kørsel fra kommandolinjen bruges argumentet `-once`.

## Byg fra kildekoden

```powershell
go test ./...
go build -buildvcs=false -trimpath -ldflags="-H windowsgui" -o SharePoint-Arkiv.exe .
```
