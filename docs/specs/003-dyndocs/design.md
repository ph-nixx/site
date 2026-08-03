## Problem

The github repo is the source of truth.

We want to automate flexible docs pages that are always synced to the source repo's default branch (e.g. `main`).
We will derive the stats and doc UI from each project via the github webhook and REST API;

Solution:

By using directory conventions and [comrak](https://docs.rs/comrak/latest/comrak/) we can automate the documentation UI and route generation for each project.
We need convention so generalizing cached doc reconciliation accross all projects yields consistent results.

Convention:

- each repo the participates in the auto doc injestion must have `docs/overview.md` at the repo root

- any child directory of `docs/` that contains strictly `.md` files is a valid docs section

- you must register the doc sections you want to be displayed and meta data about them (e.g. title) 
  in the `docs/overview.md` json or yaml front matter and the markdown following the front matter will be parsed to html 
  and treated as the project home page

- we only allow cached html strings to be added, updated or deleted on push to the default branch

- the html generated for a cached doc will be accessible at `domain.com/docs/{repo-name}/{section-name}/{file-name}`
  and all names will be slugified

The html for a route will be cached in redis as a json string using the file path relative to `docs/`
as the key. 
*Why this is not a bad idea*: I rarely write doc markdown files longer than 250 lines if one line is 50 bytes on average 
then a file equates to *12,500* bytes in size; when we parse to the markdown to html the bytes count 
may grow up to a factor of *2.5* which makes the final cached value 30KB without GZIP.
I will have no more than 5 active project at a time and if we are extreme and say 100 
markdown files per project this is a total cache size of 14.6MB (without GZIP) which is negligible.

Issues:

- [ ] buildlog is not using the global github account url making the commit url invalid

- [ ] Project and overview html in `src/components/docs.rs` are seperate <a> elements when they should be under one so effect apply to both at the same time

Planned:

- [ ] project icon provided by `.svg` files in `docs/` and registered via `docs/overview.md`

- [ ] yaml front matter in `docs/overview.md`
