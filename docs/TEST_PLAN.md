# Piano di test — bozza di lavoro

Documento di lavoro, non definitivo: raccoglie le idee di test da rivedere prima dell'implementazione.

## Unit test

- **Canonicalizzazione dell'hash della serie**
  - Stessi attributi in ordine diverso → stesso hash.
  - Stesso valore con tipo diverso (es. int `1` e string `"1"`) → **stesso** hash (scelta di design: l'hash segue la rappresentazione salvata).
  - Stessa coppia chiave/valore in gruppi diversi (Resource, datapoint) → hash diversi.
  - Confini dei campi: `"ab"+"c"` e `"a"+"bc"` → hash diversi.
  - Array/kvlist con lo stesso contenuto → stessa stringa e stesso hash (conversione deterministica).
  - Golden test: hash di un input noto fissato nel test.
  - Stessa serie da scope diversi → stesso hash.
  - Descrizione diversa → stesso hash.
  - Unità o tipo diversi → hash diversi.
  - Sum delta e Sum cumulativa con gli stessi attributi → hash diversi.
- **Mapper**
  - Un messaggio con datapoint a cavallo della mezzanotte UTC produce due righe di catalogo (`Day` diversi) per la stessa serie.
  - Più datapoint della stessa serie nello stesso giorno producono una sola riga di catalogo (deduplicazione nel messaggio).
- **Servizio con store finto**
  - Il catalogo viene scritto prima dei datapoint.
  - Se l'insert del catalogo fallisce, nessun datapoint viene scritto.

## Test di integrazione (testcontainers + ClickHouse)

- Round trip gRPC → ClickHouse con lettura del catalogo e dei datapoint collegati per hash.
- Pattern di lettura eseguiti con `EXPLAIN indexes = 1`: verificare che vengano lette solo le partizioni e i granuli dell'intervallo richiesto, come prova del requisito "nessuna scansione completa".

## Da decidere

- Quali di questi test sono prioritari entro le 3–4 ore.
- Come generare dati rappresentativi per le verifiche di layout e prestazioni.
