# dvr-tools

This is a flexible set of tools for rule-based matching of TV shows based on attributes about the file and then applying matching encoding options for the content as needed.

## Major Features

1. It allows using an [expression-based language](https://github.com/antonmedv/expr) to match attributes:

    * `Name startsWith 'Family Guy' && Height == 720`: Any Family Guy episode recorded in 720p
    * `Audio.Format != 'AC-3' || Audio.BitRate > 384000` primary audio track is not AC-3 or is higher than 384 kbps.

2. It can chain matching rules to decide how to re-encode shows

3. Automatic integration of [Comskip](https://github.com/erikkaashoek/Comskip) as desired with two primary modes of operation:

  * `comskip="chapter"` mode makes Matroska chapter files and then remuxes the content into an mkv container with the chapter file. The chapters are detected by most media players such as VLC and Plex for easy skipping. This is a good idea if you're not sure if your comskip ini is reliable at detecting commercials.
  * `comskip="true"` chops the source file up to split out the commercials and then re-encodes it into a single file 

4. Allows creating [watchlogs](#watchlogs) for commercial skipping

## System Requirements

the `videoproc` application will shell out to several common linux applications to do the bulk of encoding and commercial skipping work.

Ensure you have these:
  * [Comskip](https://github.com/erikkaashoek/Comskip)
  * [libmediainfo](https://github.com/jaeguly/libmediainfo)
  * libopenjpeg (for comskip)
  * ffmpeg 4 or higher
  * mkvtoolnix-cli

Dockerfiles may be included in the future

## Getting Started

see the `examples/` folder for some example processing config files. More docs to follow.

Basic running usage is:

```shell
videoproc --debug --config [path-to-config.toml] /path/to/file.ts
```

## Advanced Topics

### Watchlogs

Watchlogs are created by the `seeker` application. The `seeker` application's use case is for stubborn shows where commercial detection by Comskip is poor. For those shows, if you use chapter-based commercial detection, you won't accidentally delete content. And then later, when it's time to save space on your DVR, you can use the user themselves as an indication of where the commercials are.

That is, when the user pauses and then jumps forward in a video file, the seeker notes this and puts it together into a watchlog file. The seeker can use this watchlog file as data on where the commercials are.

Currently `seeker` only works with Plex Media Server, but this may be extended in the future to look at watch locations from other media players.