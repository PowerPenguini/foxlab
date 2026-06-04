# AGENTS.md

## Zakres

Te zasady dotyczą pracy w repo FoxLab, szczególnie `web/shell`, `apps/files`,
`apps/topology` oraz innych aplikacji stylizowanych na terminal.

## Styl UI

- UI ma być zwarte, terminalowe i robocze: monospace, małe odstępy, proste
  obramowania, dużo informacji na ekranie.
- Nie dodawaj landing page, hero, marketingowych opisów, kart w kartach,
  glassmorphism, dekoracyjnych gradientów, orbów, dużych radiusów ani miękkich
  cieni.
- Kontrolki mają wyglądać jak elementy terminala, nie jak webowe przyciski.
  Preferuj tekstowe komendy, nawiasy i inverse color dla aktywnego stanu.
- Nie dodawaj widocznych instrukcji UI w aplikacji, jeśli zachowanie wynika z
  kontekstu.

## Interakcje

- Hover nie może być głównym stanem UI. W Files nie ma wizualnego hovera na
  wierszach; aktywny jest tylko wybrany wiersz.
- Nie zostawiaj natywnych outline/focus ringów na elementach terminalowych.
  Focus i selection mają być reprezentowane terminalowym zaznaczeniem.
- Przyciski w popupach błędów są tekstowe i bez ramki. Wyjątkiem są kontrolki
  titlebara okien.
- Menu kontekstowe ma być ciasne i terminalowe, bez webowych kart i
  dekoracyjnych stanów.

## Files

- Nawigacja klawiaturą:
  - `j` / ArrowDown: w dół
  - `k` / ArrowUp: w górę
  - `l` / Enter: otwórz
  - `h` / Backspace: katalog wyżej
  - `r`: odśwież
- Nie przechwytuj skrótów z Ctrl, Alt ani Meta.
- Zaznaczony wiersz ma pozostać widoczny przy nawigacji.
- Katalogi w Files używają bursztynowego ANSI `#f0d58a`.
- Nieznane rozszerzenia plików nie otwierają Files przez fallback. Handler musi
  być jawny; brak handlera daje błąd.

## Otwieranie Plików

- `.lab` otwiera Topology Editor.
- Katalogi otwierają Files.
- Nowe typy plików rejestruj jawnie przez manifest handlera.
- Nie dodawaj globalnych fallback handlerów dla zwykłych plików.

## Błędy

- Błędy shell/open-file pokazuj jako normalne okna shellowego window managera,
  nie jako toast ani fixed overlay w rogu.
- Okno błędu ma titlebar, taskbar entry i zwykłe zamykanie/minimalizację.
- Okna systemowych błędów nie zapisują się do backendowego WM.
- Treść błędu ma być konkretna, bez marketingowego tonu.

## Okna i Stan

- Nie renderuj martwych okien, jeśli proces aplikacji już nie żyje.
- Układ okien powinien przetrwać zwykły refresh, ale dynamiczne porty martwych
  procesów nie mogą zostać użyte ponownie.
- Appki komunikują się z backendem aplikacji bezpośrednio. Shell jest używany
  do WM/open-file przez API/gRPC tam, gdzie to ma sens.

## Walidacja

- Shell: `npm run build` w `web`.
- Files: `npm --prefix apps/files run build` i `make package-files`.
- Topology: `npm --prefix apps/topology run build` i `make package-topology`.
- Backend/shared: `make test`.
- Ostrzeżenie Vite o Node `20.18.0` vs `20.19+` jest obecnie znane; build może
  mimo tego przejść.
