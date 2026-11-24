<p align="center"><img src="assets/icon-256.png" width="96" alt="ZTube"></p>

<h1 align="center">ZTube</h1>

<p align="center">Baixe vídeos, áudios, lives, playlists e canais do YouTube com poucos cliques.<br>Criado por <b>Rosani Ferreira</b>.</p>

---

## O que ele faz

- **Detecta o tipo de link sozinho:** vídeo, live, playlist, canal ou vídeo dentro de uma playlist.
- **Vídeo ou só áudio:** MP4 compatível com Premiere e After Effects, ou áudio em WAV, MP3, M4A, FLAC e OPUS.
- **Fatiar em partes:** separa packs de efeitos e músicas em arquivos individuais, usando os capítulos do vídeo, os tempos da descrição, o comentário fixado, as pausas de silêncio ou uma lista que você cola.
- **Lives:** grava desde o início da transmissão ou a partir de um horário.
- **Canais e playlists:** baixa tudo numa pasta própria e pula o que já está lá.
- **Fila de downloads:** pausar, continuar, cancelar e histórico do que já terminou.
- **Instala o que precisa sozinho:** a página Requisitos baixa e atualiza as ferramentas direto das páginas oficiais.
- **Tema claro e escuro.**

## Como usar

1. Baixe o `ZTube.exe` na página de [versões](../../releases).
2. Abra o arquivo. Se aparecer "O Windows protegeu o computador", clique em **Mais informações** e depois em **Executar assim mesmo** (isso acontece com programas novos sem assinatura digital paga).
3. Na primeira vez, o ZTube abre a página **Requisitos**. Clique em **Instalar e atualizar tudo**.
4. Cole o link, escolha o formato e a pasta, e clique em **Baixar**.

Funciona no Windows 10 e 11 (64 bits).

## Ferramentas usadas

O ZTube é uma interface para ferramentas de código aberto:

| Ferramenta | Para que serve | Licença |
|---|---|---|
| [yt-dlp](https://github.com/yt-dlp/yt-dlp) | Faz os downloads | Unlicense (domínio público) |
| [FFmpeg](https://ffmpeg.org) | Junta, converte e fatia os arquivos | LGPL/GPL |
| [Deno](https://deno.com) | Ajuda o yt-dlp a ler as páginas do YouTube | MIT |
| [WebView2](https://developer.microsoft.com/microsoft-edge/webview2/) | Desenha a interface (vem com o Windows) | Microsoft |

Essas ferramentas **não vêm dentro do ZTube**. Ele baixa cada uma das páginas oficiais, confere se o arquivo chegou intacto (SHA-256) e guarda tudo em `%LOCALAPPDATA%\ZTube\ferramentas`, sem mexer no resto do Windows.

## Privacidade

O ZTube não tem conta, não coleta dados e não envia nada para lugar nenhum. Ele só se conecta ao YouTube (para baixar) e ao GitHub (para buscar as ferramentas). Configurações e histórico ficam em `%LOCALAPPDATA%\ZTube`.

## Uso responsável

Os termos do YouTube não permitem baixar vídeos fora dos recursos oficiais. Use o ZTube só para conteúdo que você tem direito de baixar.

## Para desenvolvedores

Feito em Go com [go-webview2](https://github.com/jchv/go-webview2). A interface fica em `ui/index.html`.

```
go build -mod=vendor -trimpath -ldflags "-H windowsgui -s -w" -o ZTube.exe .
```

As dependências estão em `vendor/`, então a compilação não precisa de internet. Ao criar uma tag `v*` (por exemplo `v1.0.0`), o GitHub Actions gera o `ZTube.exe` e publica na página de versões.
