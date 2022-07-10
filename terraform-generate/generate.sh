foo=($(find . -iname '*.tf'))
declare -a contentarr
for i in "${foo[@]::${#foo[@]}-1}"; do
  content=$(echo "{\"name\":\""$i"\",\"content\":\"$(echo $(cat $i | base64))\"},")
  contentarr+=$content
done
for i in "${foo[@]: -1:1}"; do
  content=$(echo "{\"name\":\""$i"\",\"content\":\"$(echo $(cat $i | base64))\"}")
  contentarr+=$content
done
echo "{\"apiVersion\": \"v1\", \"kind\": \"TerraformWrapper\", \"list\": [$contentarr]}"