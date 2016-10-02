capze
======

`capze` is a tool to make [Capistrano](http://capistranorb.com/)-lile directory structure.

## Usage

```bash
$ capze --keeps 5 /tmp/app /var/www/app
$ cat /tmp/app.tar | capze --keeps 5 /var/www/app

$ capze --rollback /var/www/app
```
