// Se ejecuta UNA vez, cuando el volumen de MongoDB está vacío (docker-entrypoint-initdb.d).
// Crea el usuario de la aplicación con privilegio mínimo: solo readWrite sobre su base y
// solo desde la IP fija del coordinador en la red "datos" (equivale a la regla
// "permit tcp host <coordinador> host <mongo> eq 27017" de una ACL extendida).
const dbName = process.env.MONGO_APP_DB || "tb2_denuncias";
const user = process.env.MONGO_APP_USER || "tb2_app";
const pwd = process.env.MONGO_APP_PASSWORD;
const ip = process.env.COORDINATOR_DATOS_IP || "172.29.20.10";
if (!pwd) { throw new Error("Falta MONGO_APP_PASSWORD"); }
db.getSiblingDB(dbName).createUser({
  user: user,
  pwd: pwd,
  roles: [{ role: "readWrite", db: dbName }],
  authenticationRestrictions: [{ clientSource: [ip] }]
});
print(`Usuario ${user} creado: readWrite en ${dbName}, solo desde ${ip}`);
