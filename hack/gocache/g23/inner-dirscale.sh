# Functional check (not timing): does one withFiles of n separate single-file sources, mounted into
# an exec, work on this engine? gcexp's DirScale at several n. /work is a gcexp checkout.
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
for n in 300 500 1400; do
  if timeout 1200 dagger -s call gcexp dir-scale --n $n --salt ds-$NONCE-$n --nonce x > /out/dirscale-$n.txt 2>&1; then
    echo "n=$n ok: $(tail -1 /out/dirscale-$n.txt)" >> /out/summary.txt
  else
    echo "n=$n FAILED: $(grep -o -m1 'mount options is too long\|failed to mount[^:]*' /out/dirscale-$n.txt | head -1); lowerdirs=$(grep -o -m1 'lowerdir=[^] ]*' /out/dirscale-$n.txt | tr ':' '\n' | wc -l)" >> /out/summary.txt
  fi
done
