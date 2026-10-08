#!/usr/bin/env bash
# Uploads a small generated PNG through the admin API, reads it back, then deletes it.
# Usage: bash media-test.sh <path to logaluxe-be> [keep]
B=http://127.0.0.1:18080/v1
S="$(dirname "$0")"
PW=$(grep '^ADMIN_PASSWORD=' "$1/.env" | cut -d= -f2-)
j(){ node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{try{const o=JSON.parse(d);console.log(eval(process.argv[1]))}catch(e){console.log("PARSE_FAIL",d.slice(0,300))}})' "$1"; }
node -e '
const zlib=require("zlib"),fs=require("fs");
const W=64,H=80,raw=Buffer.alloc((W*3+1)*H);
for(let y=0;y<H;y++){raw[y*(W*3+1)]=0;for(let x=0;x<W;x++){const i=y*(W*3+1)+1+x*3;raw[i]=122+y;raw[i+1]=31+x/2;raw[i+2]=43+y/2;}}
const crc=(b)=>{let c,t=[];for(let n=0;n<256;n++){c=n;for(let k=0;k<8;k++)c=c&1?0xedb88320^(c>>>1):c>>>1;t[n]=c>>>0}let r=0xffffffff;for(const x of b)r=t[(r^x)&255]^(r>>>8);return (r^0xffffffff)>>>0};
const chunk=(t,d)=>{const l=Buffer.alloc(4);l.writeUInt32BE(d.length);const td=Buffer.concat([Buffer.from(t),d]);const c=Buffer.alloc(4);c.writeUInt32BE(crc(td));return Buffer.concat([l,td,c])};
const ihdr=Buffer.alloc(13);ihdr.writeUInt32BE(W,0);ihdr.writeUInt32BE(H,4);ihdr[8]=8;ihdr[9]=2;
fs.writeFileSync(process.argv[1],Buffer.concat([Buffer.from([137,80,78,71,13,10,26,10]),chunk("IHDR",ihdr),chunk("IDAT",zlib.deflateSync(raw)),chunk("IEND",Buffer.alloc(0))]));
' "$S/test.png"
echo "not an image" > "$S/fake.jpg"

T=$(curl -s -X POST $B/admin/login -H 'Content-Type: application/json' -d "{\"email\":\"admin@logaluxe.test\",\"password\":\"$PW\"}" | j 'o.token')
A="Authorization: Bearer $T"
echo "1 list:        $(curl -s -H "$A" $B/admin/media | j '"storage="+o.storage+" slots="+o.slots.map(s=>s.key).join(",")+" images="+o.media.length')"
echo "2 no auth:     $(curl -s -o /dev/null -w '%{http_code}' -X POST -F slot=hero -F "file=@$S/test.png" $B/admin/media)"
echo "3 fake file:   $(curl -s -X POST -H "$A" -F slot=hero -F "file=@$S/fake.jpg;type=image/jpeg" $B/admin/media | j 'o.error||o.ok')"
echo "4 bad slot:    $(curl -s -X POST -H "$A" -F slot=nope -F "file=@$S/test.png" $B/admin/media | j 'o.error||o.ok')"
UP=$(curl -s -X POST -H "$A" -F slot=hero -F alt="Upload test" -F "file=@$S/test.png" $B/admin/media)
echo "5 upload:      $(echo "$UP" | j 'o.error||("ok id="+o.id)')"
ID=$(echo "$UP" | j 'o.id')
echo "6 public list: $(curl -s "$B/site/media?slot=hero" | j 'o.media.length+" image(s), alt="+(o.media[0]||{}).alt')"
echo "7 read back:   $(curl -s -o "$S/back.png" -w '%{http_code} %{content_type} %{size_download} bytes' $B/media/$ID) (sent $(wc -c < "$S/test.png") bytes) same=$(cmp -s "$S/test.png" "$S/back.png" && echo yes || echo no)"
echo "8 hide:        $(curl -s -X PUT -H "$A" -H 'Content-Type: application/json' -d '{"active":false}' $B/admin/media/$ID | j 'o.error||o.ok') -> public now $(curl -s "$B/site/media?slot=hero" | j 'o.media.length')"
if [ "$2" != "keep" ]; then
  echo "9 delete:      $(curl -s -X DELETE -H "$A" $B/admin/media/$ID | j 'o.error||o.ok') -> file now $(curl -s -o /dev/null -w '%{http_code}' $B/media/$ID)"
fi
